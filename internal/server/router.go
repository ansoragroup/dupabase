package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apiAuth "github.com/ansoraGROUP/dupabase/internal/api/auth"
	apiRest "github.com/ansoraGROUP/dupabase/internal/api/rest"
	"github.com/ansoraGROUP/dupabase/internal/database"
	dupaHTTP "github.com/ansoraGROUP/dupabase/internal/httputil"
	"github.com/ansoraGROUP/dupabase/internal/middleware"
	"github.com/ansoraGROUP/dupabase/internal/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	mux              *http.ServeMux
	authService      *platform.AuthService
	projectService   *platform.ProjectService
	credService      *platform.CredentialService
	auditService     *platform.AuditService
	backupService    *platform.BackupService
	importService    *platform.ImportService
	adminService     *platform.AdminService
	orgService       *platform.OrgService
	analyticsService *platform.AnalyticsService
	tableService     *platform.TableService
	sqlService       *platform.SQLService
	authUserService  *platform.AuthUserService
	platformAuth     *middleware.PlatformAuth
	projectRouter    *middleware.ProjectRouter
	supabaseAuth     *apiAuth.Handler
	supabaseRest     *apiRest.Handler
	authLimiter      *middleware.RateLimiter // 5 req/s, burst 10 for auth endpoints
	apiLimiter       *middleware.RateLimiter // 30 req/s, burst 60 for API endpoints
	platformDB       *pgxpool.Pool
	importMaxBytes   int64
	importTempDir    string
	trustProxy       bool
	dashboardProxy   http.Handler
}

func New(
	authService *platform.AuthService,
	projectService *platform.ProjectService,
	credService *platform.CredentialService,
	auditService *platform.AuditService,
	backupService *platform.BackupService,
	importService *platform.ImportService,
	adminService *platform.AdminService,
	orgService *platform.OrgService,
	analyticsService *platform.AnalyticsService,
	tableService *platform.TableService,
	sqlService *platform.SQLService,
	authUserService *platform.AuthUserService,
	poolManager *database.PoolManager,
	platformDB *pgxpool.Pool,
	importMaxSizeMB int,
	importTempDir string,
	trustProxy bool,
) *Server {
	s := &Server{
		mux:              http.NewServeMux(),
		authService:      authService,
		projectService:   projectService,
		credService:      credService,
		auditService:     auditService,
		backupService:    backupService,
		importService:    importService,
		adminService:     adminService,
		orgService:       orgService,
		analyticsService: analyticsService,
		tableService:     tableService,
		sqlService:       sqlService,
		authUserService:  authUserService,
		platformAuth:     middleware.NewPlatformAuth(authService),
		projectRouter:    middleware.NewProjectRouter(poolManager),
		supabaseAuth:     apiAuth.NewHandler(),
		supabaseRest:     apiRest.NewHandler(),
		authLimiter:      middleware.NewRateLimiter(5, 10, trustProxy),  // 5 req/s, burst 10
		apiLimiter:       middleware.NewRateLimiter(30, 60, trustProxy), // 30 req/s, burst 60
		platformDB:       platformDB,
		importMaxBytes:   int64(importMaxSizeMB) * 1024 * 1024,
		importTempDir:    importTempDir,
		trustProxy:       trustProxy,
	}

	// Set up dashboard reverse proxy if DASHBOARD_URL is configured
	// In Docker, Next.js standalone runs on port 3000 internally
	if dashURL := os.Getenv("DASHBOARD_URL"); dashURL != "" {
		target, err := url.Parse(dashURL)
		if err == nil {
			proxy := httputil.NewSingleHostReverseProxy(target)
			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				slog.Warn("Dashboard proxy error", "error", err, "path", r.URL.Path)
				http.Error(w, "Dashboard unavailable", http.StatusBadGateway)
			}
			s.dashboardProxy = proxy
			slog.Info("Dashboard proxy enabled", "target", dashURL)
		}
	}

	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return middleware.RequestID(securityHeaders(cors(s.mux), s.trustProxy))
}

// Stop releases resources held by the server (rate limiters, etc.).
func (s *Server) Stop() {
	s.authLimiter.Stop()
	s.apiLimiter.Stop()
}

// securityHeaders adds security headers to every response.
func securityHeaders(next http.Handler, trustProxy bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// Use strict CSP for API routes, relaxed CSP for dashboard
		if isAPIRoute(r.URL.Path) {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		} else {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.jsdelivr.net blob:; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: blob:; font-src 'self' data: https://cdn.jsdelivr.net; connect-src 'self' https://cdn.jsdelivr.net; worker-src 'self' blob:; frame-ancestors 'none'")
		}

		// HSTS — enable in production behind TLS.
		// Only trust X-Forwarded-Proto when trustProxy is set, since
		// the header can be spoofed by untrusted clients.
		isTLS := r.TLS != nil
		if !isTLS && trustProxy {
			isTLS = r.Header.Get("X-Forwarded-Proto") == "https"
		}
		if isTLS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// isAPIRoute returns true for API/platform/auth/rest routes.
func isAPIRoute(path string) bool {
	return strings.HasPrefix(path, "/platform/") ||
		strings.HasPrefix(path, "/auth/v1/") ||
		strings.HasPrefix(path, "/rest/v1/") ||
		path == "/health"
}

// maxBody limits request body size to prevent DoS via large payloads.
func maxBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) registerRoutes() {
	// Health check with DB ping
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := s.platformDB.Ping(r.Context()); err != nil {
			slog.Error("health check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "revision": BuildRevision})
	})

	// Platform auth (no auth required, rate-limited)
	s.mux.Handle("POST /platform/auth/register", s.authLimiter.Middleware(maxBody(http.HandlerFunc(s.handlePlatformRegister), 1<<20)))
	s.mux.Handle("POST /platform/auth/login", s.authLimiter.Middleware(maxBody(http.HandlerFunc(s.handlePlatformLogin), 1<<20)))

	// Platform endpoints (require platform JWT)
	s.mux.Handle("GET /platform/auth/me", s.platformAuth.Middleware(http.HandlerFunc(s.handlePlatformMe)))
	s.mux.Handle("GET /platform/projects", s.platformAuth.Middleware(http.HandlerFunc(s.handleListProjects)))
	s.mux.Handle("GET /platform/projects/{id}", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetProject)))
	s.mux.Handle("POST /platform/projects", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleCreateProject), 1<<20)))
	s.mux.Handle("DELETE /platform/projects/{id}", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleDeleteProject), 1<<20)))
	s.mux.Handle("PUT /platform/auth/password", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleChangePassword), 1<<20)))
	s.mux.Handle("PATCH /platform/projects/{id}/settings", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleUpdateProjectSettings), 1<<20)))
	s.mux.Handle("POST /platform/credentials/reveal", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleRevealCredentials), 1<<20)))
	s.mux.Handle("POST /platform/projects/{id}/rotate-keys", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleRotateAPIKeys), 1<<20)))

	// Import endpoints (require platform JWT)
	s.mux.Handle("POST /platform/projects/{id}/import", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleStartImport), s.importMaxBytes)))
	s.mux.Handle("GET /platform/projects/{id}/import/{taskId}", s.platformAuth.Middleware(http.HandlerFunc(s.handleImportStatus)))
	s.mux.Handle("GET /platform/projects/{id}/import/history", s.platformAuth.Middleware(http.HandlerFunc(s.handleImportHistory)))
	s.mux.Handle("POST /platform/projects/{id}/import/{taskId}/cancel", s.platformAuth.Middleware(http.HandlerFunc(s.handleCancelImport)))
	s.mux.Handle("POST /platform/projects/{id}/import/analyze", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleAnalyzeDump), s.importMaxBytes)))

	// Backup endpoints (require platform JWT)
	s.mux.Handle("POST /platform/backups/settings", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleSaveBackupSettings), 1<<20)))
	s.mux.Handle("GET /platform/backups/settings", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetBackupSettings)))
	s.mux.Handle("GET /platform/backups/history", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetBackupHistory)))
	s.mux.Handle("POST /platform/backups/run", s.platformAuth.Middleware(http.HandlerFunc(s.handleRunBackupNow)))
	s.mux.Handle("POST /platform/backups/test-connection", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleTestS3Connection), 1<<20)))
	s.mux.Handle("PATCH /platform/backups/settings", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleToggleBackup), 1<<20)))
	s.mux.Handle("POST /platform/backups/{historyId}/restore", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleRestoreBackup), 1<<20)))

	// Export endpoint (require platform JWT + org developer role)
	s.mux.Handle("GET /platform/projects/{id}/export", s.platformAuth.Middleware(http.HandlerFunc(s.handleExportDatabase)))

	// Public: registration mode (for frontend to check)
	s.mux.HandleFunc("GET /platform/auth/registration-mode", s.handleRegistrationMode)

	// Admin endpoints (require platform JWT + is_admin)
	s.mux.Handle("GET /platform/admin/users", s.platformAuth.Middleware(s.adminOnly(http.HandlerFunc(s.handleAdminListUsers))))
	s.mux.Handle("DELETE /platform/admin/users/{id}", s.platformAuth.Middleware(s.adminOnly(http.HandlerFunc(s.handleAdminDeleteUser))))
	s.mux.Handle("GET /platform/admin/settings", s.platformAuth.Middleware(s.adminOnly(http.HandlerFunc(s.handleAdminGetSettings))))
	s.mux.Handle("PUT /platform/admin/settings", s.platformAuth.Middleware(s.adminOnly(maxBody(http.HandlerFunc(s.handleAdminUpdateSettings), 1<<20))))
	s.mux.Handle("GET /platform/admin/invites", s.platformAuth.Middleware(s.adminOnly(http.HandlerFunc(s.handleAdminListInvites))))
	s.mux.Handle("POST /platform/admin/invites", s.platformAuth.Middleware(s.adminOnly(maxBody(http.HandlerFunc(s.handleAdminCreateInvite), 1<<20))))
	s.mux.Handle("DELETE /platform/admin/invites/{id}", s.platformAuth.Middleware(s.adminOnly(http.HandlerFunc(s.handleAdminDeleteInvite))))

	// Organization endpoints (require platform JWT)
	s.mux.Handle("POST /platform/orgs", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleCreateOrg), 1<<20)))
	s.mux.Handle("GET /platform/orgs", s.platformAuth.Middleware(http.HandlerFunc(s.handleListOrgs)))
	s.mux.Handle("GET /platform/orgs/{id}", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetOrg)))
	s.mux.Handle("PATCH /platform/orgs/{id}", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleUpdateOrg), 1<<20)))
	s.mux.Handle("DELETE /platform/orgs/{id}", s.platformAuth.Middleware(http.HandlerFunc(s.handleDeleteOrg)))
	s.mux.Handle("GET /platform/orgs/{id}/members", s.platformAuth.Middleware(http.HandlerFunc(s.handleListOrgMembers)))
	s.mux.Handle("POST /platform/orgs/{id}/invites", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleCreateOrgInvite), 1<<20)))
	s.mux.Handle("POST /platform/orgs/invites/{token}/accept", s.platformAuth.Middleware(http.HandlerFunc(s.handleAcceptOrgInvite)))
	s.mux.Handle("DELETE /platform/orgs/{id}/members/{uid}", s.platformAuth.Middleware(http.HandlerFunc(s.handleRemoveOrgMember)))
	s.mux.Handle("PATCH /platform/orgs/{id}/members/{uid}", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleUpdateOrgMemberRole), 1<<20)))
	s.mux.Handle("GET /platform/orgs/{id}/invites", s.platformAuth.Middleware(http.HandlerFunc(s.handleListOrgInvites)))
	s.mux.Handle("DELETE /platform/orgs/{id}/invites/{iid}", s.platformAuth.Middleware(http.HandlerFunc(s.handleRevokeOrgInvite)))
	s.mux.Handle("POST /platform/orgs/{id}/leave", s.platformAuth.Middleware(http.HandlerFunc(s.handleLeaveOrg)))

	// Analytics endpoints (require platform JWT + org viewer role)
	s.mux.Handle("GET /platform/projects/{id}/analytics/database", s.platformAuth.Middleware(http.HandlerFunc(s.handleDatabaseAnalytics)))
	s.mux.Handle("GET /platform/projects/{id}/analytics/connections", s.platformAuth.Middleware(http.HandlerFunc(s.handleConnectionAnalytics)))
	s.mux.Handle("GET /platform/projects/{id}/analytics/queries", s.platformAuth.Middleware(http.HandlerFunc(s.handleQueryAnalytics)))
	s.mux.Handle("GET /platform/projects/{id}/analytics/auth", s.platformAuth.Middleware(http.HandlerFunc(s.handleAuthAnalytics)))
	s.mux.Handle("GET /platform/projects/{id}/analytics/api-usage", s.platformAuth.Middleware(http.HandlerFunc(s.handleAPIUsageAnalytics)))
	s.mux.Handle("GET /platform/projects/{id}/analytics/overview", s.platformAuth.Middleware(http.HandlerFunc(s.handleOverviewAnalytics)))

	// Table browser endpoints (require platform JWT + org viewer role)
	s.mux.Handle("GET /platform/projects/{id}/tables", s.platformAuth.Middleware(http.HandlerFunc(s.handleListTables)))
	s.mux.Handle("GET /platform/projects/{id}/tables/{table}/columns", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetTableColumns)))
	s.mux.Handle("GET /platform/projects/{id}/tables/{table}/rows", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetTableRows)))
	s.mux.Handle("POST /platform/projects/{id}/tables/{table}/rows", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleInsertRow), 1<<20)))
	s.mux.Handle("PATCH /platform/projects/{id}/tables/{table}/rows", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleUpdateRow), 1<<20)))
	s.mux.Handle("DELETE /platform/projects/{id}/tables/{table}/rows", s.platformAuth.Middleware(http.HandlerFunc(s.handleDeleteRow)))

	// SQL editor endpoint (require platform JWT + org developer role)
	s.mux.Handle("POST /platform/projects/{id}/sql", s.platformAuth.Middleware(maxBody(http.HandlerFunc(s.handleExecuteSQL), 1<<20)))

	// Auth user management endpoints (require platform JWT + org admin role)
	s.mux.Handle("GET /platform/projects/{id}/auth/users", s.platformAuth.Middleware(http.HandlerFunc(s.handleListAuthUsers)))
	s.mux.Handle("GET /platform/projects/{id}/auth/users/{uid}", s.platformAuth.Middleware(http.HandlerFunc(s.handleGetAuthUser)))
	s.mux.Handle("DELETE /platform/projects/{id}/auth/users/{uid}", s.platformAuth.Middleware(http.HandlerFunc(s.handleDeleteAuthUser)))
	s.mux.Handle("POST /platform/projects/{id}/auth/users/{uid}/ban", s.platformAuth.Middleware(http.HandlerFunc(s.handleBanAuthUser)))
	s.mux.Handle("POST /platform/projects/{id}/auth/users/{uid}/unban", s.platformAuth.Middleware(http.HandlerFunc(s.handleUnbanAuthUser)))

	// Logs endpoint (require platform JWT + org viewer role)
	s.mux.Handle("GET /platform/projects/{id}/logs", s.platformAuth.Middleware(http.HandlerFunc(s.handleProjectLogs)))

	// Supabase-compatible API (routed per-project via apikey JWT, rate-limited)
	s.mux.Handle("POST /auth/v1/signup", s.authLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.Signup)), 1<<20)))
	s.mux.Handle("POST /auth/v1/token", s.authLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.Token)), 1<<20)))
	s.mux.Handle("GET /auth/v1/user", s.apiLimiter.Middleware(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.GetUser))))
	s.mux.Handle("PUT /auth/v1/user", s.apiLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.UpdateUser)), 1<<20)))
	s.mux.Handle("POST /auth/v1/logout", s.apiLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.Logout)), 1<<20)))
	s.mux.Handle("DELETE /auth/v1/admin/users/{id}", s.authLimiter.Middleware(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseAuth.AdminDeleteUser))))

	// PostgREST-compatible REST API (rate-limited, body limit for mutating requests)
	s.mux.Handle("/rest/v1/rpc/", s.apiLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseRest.HandleRPC)), 1<<20)))
	s.mux.Handle("/rest/v1/", s.apiLimiter.Middleware(maxBody(s.projectRouter.Middleware(http.HandlerFunc(s.supabaseRest.HandleTable)), 1<<20)))

	// Dashboard proxy: forward all non-API requests to the Next.js server
	if s.dashboardProxy != nil {
		s.mux.Handle("/", s.dashboardProxy)
	}
}

// ---------- Platform handlers ----------

func (s *Server) handlePlatformRegister(w http.ResponseWriter, r *http.Request) {
	var req platform.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, status, err := s.authService.Register(r.Context(), req)
	if err != nil {
		s.auditService.Log(r.Context(), nil, "register_failed", "user", "", r, map[string]interface{}{"email": req.Email})
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &resp.User.ID, "register", "user", resp.User.ID, r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handlePlatformLogin(w http.ResponseWriter, r *http.Request) {
	var req platform.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, status, err := s.authService.Login(r.Context(), req)
	if err != nil {
		s.auditService.Log(r.Context(), nil, "login_failed", "user", "", r, map[string]interface{}{"email": req.Email})
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &resp.User.ID, "login", "user", resp.User.ID, r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handlePlatformMe(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	user, err := s.authService.GetUser(r.Context(), userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	dupaHTTP.WriteJSON(w, http.StatusOK, user)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.URL.Query().Get("org_id")

	// If no org_id provided, use personal org for backward compat
	if orgID == "" {
		var err error
		orgID, err = s.orgService.GetPersonalOrgID(r.Context(), userID)
		if err != nil {
			slog.Error("failed to get personal org", "error", err, "handler", "handleListProjects")
			dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	} else if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org_id format"})
		return
	}

	// Verify user has viewer+ role in the org
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	projects, err := s.projectService.ListProjects(r.Context(), orgID)
	if err != nil {
		slog.Error("failed to list projects", "error", err, "handler", "handleListProjects")
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		for i := range projects {
			projects[i].ServiceRoleKey = ""
			projects[i].JWTSecret = ""
		}
	}
	dupaHTTP.WriteJSON(w, http.StatusOK, projects)

}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Look up project's org_id and verify viewer+ role
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	project, status, err := s.projectService.GetProject(r.Context(), orgID, projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		project.ServiceRoleKey = ""
		project.JWTSecret = ""
	}
	dupaHTTP.WriteJSON(w, status, project)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// If no org_id provided, use personal org for backward compat
	if req.OrgID == "" {
		personalOrgID, err := s.orgService.GetPersonalOrgID(r.Context(), userID)
		if err != nil {
			slog.Error("failed to get personal org", "error", err, "handler", "handleCreateProject")
			dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		req.OrgID = personalOrgID
	} else if !isValidUUID(req.OrgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org_id format"})
		return
	}

	// Verify user has developer+ role in the org
	role, err := s.orgService.CheckOrgRole(r.Context(), req.OrgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	resp, status, err := s.projectService.CreateProject(r.Context(), userID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "create_project", "project", resp.ID, r, map[string]interface{}{"name": req.Name, "org_id": req.OrgID})
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Look up project's org_id and verify admin+ role
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	status, err := s.projectService.DeleteProject(r.Context(), orgID, projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "delete_project", "project", projectID, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, status, err := s.authService.ChangePassword(r.Context(), userID, req)
	if err != nil {
		s.auditService.Log(r.Context(), &userID, "password_change_failed", "user", userID, r, nil)
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "password_changed", "user", userID, r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleUpdateProjectSettings(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Look up project's org_id and verify admin+ role
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	var req platform.UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, status, err := s.projectService.UpdateProjectSettings(r.Context(), orgID, projectID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "update_project_settings", "project", projectID, r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleRotateAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Look up project's org_id and verify admin+ role
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	resp, status, err := s.projectService.RotateAPIKeys(r.Context(), orgID, projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "rotate_api_keys", "project", projectID, r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleRevealCredentials(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.RevealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp, status, err := s.credService.RevealCredentials(r.Context(), userID, req)
	if err != nil {
		s.auditService.Log(r.Context(), &userID, "reveal_credentials_failed", "credentials", "", r, nil)
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "reveal_credentials", "credentials", "", r, nil)
	dupaHTTP.WriteJSON(w, status, resp)
}

// ---------- Organization handlers ----------

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.CreateOrgRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	org, status, err := s.orgService.CreateOrg(r.Context(), userID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "create_org", "organization", org.ID, r, map[string]interface{}{"name": req.Name, "slug": req.Slug})
	dupaHTTP.WriteJSON(w, status, org)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgs, err := s.orgService.ListOrgs(r.Context(), userID)
	if err != nil {
		slog.Error("failed to list orgs", "error", err, "handler", "handleListOrgs")
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	dupaHTTP.WriteJSON(w, http.StatusOK, orgs)
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Check viewer+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	org, status, err := s.orgService.GetOrg(r.Context(), orgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	org.Role = role
	dupaHTTP.WriteJSON(w, status, org)
}

func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	var req platform.UpdateOrgRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	org, status, err := s.orgService.UpdateOrg(r.Context(), orgID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "update_org", "organization", orgID, r, nil)
	dupaHTTP.WriteJSON(w, status, org)
}

func (s *Server) handleDeleteOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Verify caller is owner
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "owner") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "only the owner can delete an organization"})
		return
	}

	status, err := s.orgService.DeleteOrg(r.Context(), orgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "delete_org", "organization", orgID, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

func (s *Server) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Check viewer+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	members, err := s.orgService.ListMembers(r.Context(), orgID)
	if err != nil {
		slog.Error("failed to list org members", "error", err, "handler", "handleListOrgMembers")
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	dupaHTTP.WriteJSON(w, http.StatusOK, members)
}

func (s *Server) handleCreateOrgInvite(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	var req platform.CreateOrgInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	invite, status, err := s.orgService.CreateInvite(r.Context(), orgID, userID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "create_org_invite", "org_invite", invite.ID, r, map[string]interface{}{"org_id": orgID, "email": req.Email})
	dupaHTTP.WriteJSON(w, status, invite)
}

func (s *Server) handleAcceptOrgInvite(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	token := r.PathValue("token")
	if token == "" {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}

	org, status, err := s.orgService.AcceptInvite(r.Context(), userID, token)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "accept_org_invite", "organization", org.ID, r, nil)
	dupaHTTP.WriteJSON(w, status, org)
}

func (s *Server) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	targetUID := r.PathValue("uid")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}
	if !isValidUUID(targetUID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	if userID == targetUID {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot remove yourself, use POST /platform/orgs/{id}/leave"})
		return
	}

	status, err := s.orgService.RemoveMember(r.Context(), orgID, targetUID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "remove_org_member", "org_member", targetUID, r, map[string]interface{}{"org_id": orgID})
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "removed"})
}

func (s *Server) handleUpdateOrgMemberRole(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	targetUID := r.PathValue("uid")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}
	if !isValidUUID(targetUID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	status, err := s.orgService.UpdateMemberRole(r.Context(), orgID, targetUID, req.Role)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "update_org_member_role", "org_member", targetUID, r, map[string]interface{}{"org_id": orgID, "role": req.Role})
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "updated"})
}

func (s *Server) handleListOrgInvites(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	invites, err := s.orgService.ListInvites(r.Context(), orgID)
	if err != nil {
		slog.Error("failed to list org invites", "error", err, "handler", "handleListOrgInvites")
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	dupaHTTP.WriteJSON(w, http.StatusOK, invites)
}

func (s *Server) handleRevokeOrgInvite(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	inviteID := r.PathValue("iid")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}
	if !isValidUUID(inviteID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid invite ID format"})
		return
	}

	// Check admin+ role
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	status, err := s.orgService.RevokeInvite(r.Context(), orgID, inviteID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "revoke_org_invite", "org_invite", inviteID, r, map[string]interface{}{"org_id": orgID})
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "revoked"})
}

func (s *Server) handleLeaveOrg(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID := r.PathValue("id")
	if !isValidUUID(orgID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid org ID format"})
		return
	}

	status, err := s.orgService.LeaveOrg(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "leave_org", "organization", orgID, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "left"})
}

// ---------- Backup handlers ----------

func (s *Server) handleSaveBackupSettings(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.SaveBackupSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	orgID, err := s.resolveBackupOrgID(r, userID, req.OrgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	resp, status, err := s.backupService.SaveSettings(r.Context(), userID, orgID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "save_backup_settings", "backup", "", r, map[string]interface{}{"org_id": orgID})
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleGetBackupSettings(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID, err := s.resolveBackupOrgID(r, userID, r.URL.Query().Get("org_id"))
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	resp, status, err := s.backupService.GetSettings(r.Context(), orgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleGetBackupHistory(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID, err := s.resolveBackupOrgID(r, userID, r.URL.Query().Get("org_id"))
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	resp, status, err := s.backupService.GetHistory(r.Context(), orgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleRunBackupNow(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	orgID, err := s.resolveBackupOrgID(r, userID, r.URL.Query().Get("org_id"))
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	status, err := s.backupService.RunBackupForOrg(r.Context(), userID, orgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "run_backup_now", "backup", "", r, map[string]interface{}{"org_id": orgID})
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "backup started"})
}

func (s *Server) handleTestS3Connection(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.TestS3ConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Verify org membership (admin+)
	orgID, err := s.resolveBackupOrgID(r, userID, req.OrgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	status, err := s.backupService.TestS3Connection(r.Context(), req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	dupaHTTP.WriteJSON(w, http.StatusOK, map[string]string{"status": "connected"})
}

func (s *Server) handleToggleBackup(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var req platform.ToggleBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Verify org membership (admin+)
	orgID, err := s.resolveBackupOrgID(r, userID, req.OrgID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	resp, status, err := s.backupService.ToggleEnabled(r.Context(), userID, orgID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	action := "disable_backup"
	if req.Enabled {
		action = "enable_backup"
	}
	s.auditService.Log(r.Context(), &userID, action, "backup", "", r, map[string]interface{}{"org_id": orgID})
	dupaHTTP.WriteJSON(w, status, resp)
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	historyIDStr := r.PathValue("historyId")
	historyID, err := strconv.ParseInt(historyIDStr, 10, 64)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid history ID"})
		return
	}

	var req platform.RestoreBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Look up project_id from backup history, then verify org membership (admin+)
	var projectID string
	err = s.platformDB.QueryRow(r.Context(),
		`SELECT project_id FROM platform.backup_history WHERE id = $1`, historyID).Scan(&projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "backup history record not found"})
		return
	}
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return
	}

	taskID, status, err := s.backupService.RestoreBackup(r.Context(), userID, historyID, req.PlatformPassword)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "restore_backup", "backup", historyIDStr, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]interface{}{"task_id": taskID, "status": "restoring"})
}

func (s *Server) handleExportDatabase(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Verify org membership (developer+)
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "custom"
	}
	if format != "custom" && format != "sql" && format != "plain" {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "format must be 'custom' or 'sql'"})
		return
	}

	// Build ExportOptions from query params
	opts := platform.ExportOptions{Format: format}
	if q.Get("schema_only") == "true" {
		opts.SchemaOnly = true
	}
	if q.Get("data_only") == "true" {
		opts.DataOnly = true
	}
	if q.Get("exclude_auth") == "true" {
		opts.ExcludeAuth = true
	}
	if q.Get("inserts") == "true" {
		opts.Inserts = true
	}
	if tablesStr := q.Get("tables"); tablesStr != "" {
		opts.Tables = strings.Split(tablesStr, ",")
	}
	if excludeStr := q.Get("exclude_tables"); excludeStr != "" {
		opts.ExcludeTables = strings.Split(excludeStr, ",")
	}
	if compressStr := q.Get("compress"); compressStr != "" {
		if c, err := strconv.Atoi(compressStr); err == nil {
			opts.Compress = c
		}
	}

	dbName, err := s.backupService.GetProjectDBName(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	// Extend write deadline to 30 minutes for large exports
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Minute)); err != nil {
		slog.Warn("failed to extend write deadline", "error", err)
	}

	// Use a 30-minute context for the export
	exportCtx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	reader, filename, err := s.backupService.ExportDatabase(exportCtx, dbName, opts)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("export failed: %v", err)})
		return
	}
	defer reader.Close()

	stream := bufio.NewReader(reader)
	if _, err := stream.Peek(1); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "export failed: no dump data produced"})
		return
	}

	contentType := "application/octet-stream"
	if format == "sql" || format == "plain" {
		contentType = "application/sql"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition")
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, stream); err != nil {
		slog.Error("export stream error", "project_id", projectID, "error", err)
		panic(http.ErrAbortHandler)
	}
	if err := reader.Close(); err != nil {
		slog.Error("export command failed", "project_id", projectID, "error", err)
		panic(http.ErrAbortHandler)
	}

	s.auditService.Log(r.Context(), &userID, "export_database", "project", projectID, r, map[string]interface{}{"format": format})
}

// ---------- Import handlers ----------

func (s *Server) handleStartImport(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Verify org membership (developer+) via project's org_id
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB in-memory limit
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large or invalid multipart form"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	// Sanitize filename to prevent path traversal
	header.Filename = filepath.Base(header.Filename)

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowed := map[string]bool{".sql": true, ".dump": true, ".backup": true, ".tar": true}
	if !allowed[ext] {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported file type, use .sql, .dump, .backup, or .tar"})
		return
	}

	// Parse import options from form fields
	opts := platform.ImportOptions{
		CleanImport:     r.FormValue("clean_import") == "true",
		SkipAuthSchema:  r.FormValue("skip_auth_schema") != "false", // default true
		DisableTriggers: r.FormValue("disable_triggers") != "false", // default true
	}

	// Save file to temp directory
	dir := filepath.Join(s.importTempDir, projectID)
	if err := os.MkdirAll(dir, 0750); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create temp directory"})
		return
	}

	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate unique ID"})
		return
	}
	destName := fmt.Sprintf("%s_%s", hex.EncodeToString(randBytes), header.Filename)
	destPath := filepath.Join(dir, destName)

	dest, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	written, err := io.Copy(dest, file)
	dest.Close()
	if err != nil {
		os.Remove(destPath)
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}

	// Start import
	task, status, err := s.importService.StartImport(r.Context(), userID, projectID, destPath, header.Filename, written, opts)
	if err != nil {
		os.Remove(destPath)
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "start_import", "import", fmt.Sprintf("%d", task.ID), r, map[string]interface{}{
		"file_name": header.Filename,
		"file_size": written,
	})
	dupaHTTP.WriteJSON(w, status, task)
}

func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}
	taskIDStr := r.PathValue("taskId")

	taskID, err := strconv.ParseInt(taskIDStr, 10, 64)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task ID"})
		return
	}

	// Verify org membership (viewer+) via project's org_id
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	task, status, err := s.importService.GetImportStatus(r.Context(), projectID, taskID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	dupaHTTP.WriteJSON(w, status, task)
}

func (s *Server) handleImportHistory(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Verify org membership (viewer+) via project's org_id
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tasks, status, err := s.importService.GetImportHistory(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	dupaHTTP.WriteJSON(w, status, tasks)
}

func (s *Server) handleCancelImport(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}
	taskIDStr := r.PathValue("taskId")

	taskID, err := strconv.ParseInt(taskIDStr, 10, 64)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task ID"})
		return
	}

	// Verify org membership (developer+) via project's org_id
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	// Verify that the import task belongs to the specified project before cancelling.
	var taskProjectID string
	err = s.platformDB.QueryRow(r.Context(),
		`SELECT project_id FROM platform.import_tasks WHERE id = $1`,
		taskID).Scan(&taskProjectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "import task not found"})
		return
	}
	if taskProjectID != projectID {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "import task does not belong to this project"})
		return
	}

	status, err := s.importService.CancelImport(r.Context(), taskID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &userID, "cancel_import", "import", taskIDStr, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "cancelled"})
}

func (s *Server) handleAnalyzeDump(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	// Verify org membership (developer+) via project's org_id
	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "developer access required"})
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(s.importMaxBytes); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large or invalid form"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "file field required"})
		return
	}
	defer file.Close()

	// Save to temp file
	tmpFile, err := os.CreateTemp(s.importTempDir, "analyze-*.dump")
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create temp file"})
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save file"})
		return
	}
	tmpFile.Close()

	analysis, status, err := s.importService.AnalyzeDump(tmpFile.Name())
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	// Add file info
	type AnalysisResponse struct {
		*platform.DumpAnalysis
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}

	dupaHTTP.WriteJSON(w, status, AnalysisResponse{
		DumpAnalysis: analysis,
		FileName:     header.Filename,
		FileSize:     header.Size,
	})
}

// ---------- Admin middleware ----------

func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := middleware.GetUserID(r)
		if !s.authService.IsAdmin(r.Context(), userID) {
			dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- Registration mode (public) ----------

func (s *Server) handleRegistrationMode(w http.ResponseWriter, r *http.Request) {
	mode := s.authService.GetRegistrationMode(r.Context())
	dupaHTTP.WriteJSON(w, http.StatusOK, map[string]string{"registration_mode": mode})
}

// ---------- Admin handlers ----------

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}

	result, status, err := s.adminService.ListUsers(r.Context(), page, perPage)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	callerID := middleware.GetUserID(r)
	targetID := r.PathValue("id")
	if !isValidUUID(targetID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user ID format"})
		return
	}

	status, err := s.adminService.DeleteUser(r.Context(), callerID, targetID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &callerID, "admin_delete_user", "user", targetID, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, status, err := s.adminService.GetSettings(r.Context())
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, settings)
}

func (s *Server) handleAdminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	callerID := middleware.GetUserID(r)
	var req platform.PlatformSettings
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	status, err := s.adminService.UpdateSettings(r.Context(), req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &callerID, "admin_update_settings", "settings", "", r, map[string]interface{}{"registration_mode": req.RegistrationMode})
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "updated"})
}

func (s *Server) handleAdminListInvites(w http.ResponseWriter, r *http.Request) {
	invites, status, err := s.adminService.ListInvites(r.Context())
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, invites)
}

func (s *Server) handleAdminCreateInvite(w http.ResponseWriter, r *http.Request) {
	callerID := middleware.GetUserID(r)
	var req platform.CreateInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	invite, status, err := s.adminService.CreateInvite(r.Context(), callerID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &callerID, "admin_create_invite", "invite", invite.ID, r, nil)
	dupaHTTP.WriteJSON(w, status, invite)
}

func (s *Server) handleAdminDeleteInvite(w http.ResponseWriter, r *http.Request) {
	callerID := middleware.GetUserID(r)
	inviteID := r.PathValue("id")

	status, err := s.adminService.DeleteInvite(r.Context(), inviteID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	s.auditService.Log(r.Context(), &callerID, "admin_delete_invite", "invite", inviteID, r, nil)
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

// ---------- Analytics handlers ----------

func (s *Server) handleDatabaseAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetDatabaseAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get database analytics", "error", err, "handler", "handleDatabaseAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get database analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleConnectionAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetConnectionAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get connection analytics", "error", err, "handler", "handleConnectionAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get connection analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleQueryAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetQueryAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get query analytics", "error", err, "handler", "handleQueryAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get query analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleAuthAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetAuthAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get auth analytics", "error", err, "handler", "handleAuthAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get auth analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleAPIUsageAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetAPIUsageAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get api usage analytics", "error", err, "handler", "handleAPIUsageAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get api usage analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleOverviewAnalytics(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.analyticsService.GetOverviewAnalytics(r.Context(), projectID)
	if err != nil {
		slog.Error("failed to get overview analytics", "error", err, "handler", "handleOverviewAnalytics")
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": "failed to get overview analytics"})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

// ---------- Table browser handlers ----------

func (s *Server) handleListTables(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tables, status, err := s.tableService.ListTables(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, tables)
}

func (s *Server) handleGetTableColumns(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tableName := r.PathValue("table")
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = "public"
	}

	columns, status, err := s.tableService.GetTableColumns(r.Context(), projectID, schema, tableName)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, columns)
}

func (s *Server) handleGetTableRows(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tableName := r.PathValue("table")
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = "public"
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	orderBy := r.URL.Query().Get("order_by")
	orderDir := r.URL.Query().Get("order_dir")

	result, status, err := s.tableService.GetTableRows(r.Context(), projectID, schema, tableName, page, perPage, orderBy, orderDir)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleInsertRow(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tableName := r.PathValue("table")
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = "public"
	}

	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	result, status, err := s.tableService.InsertRow(r.Context(), projectID, schema, tableName, data)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleUpdateRow(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tableName := r.PathValue("table")
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = "public"
	}
	pkColumn := r.URL.Query().Get("pk_column")
	pkValue := r.URL.Query().Get("pk_value")
	if pkColumn == "" || !r.URL.Query().Has("pk_value") {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "pk_column and pk_value query params required"})
		return
	}

	var data map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	status, err := s.tableService.UpdateRow(r.Context(), projectID, schema, tableName, pkColumn, pkValue, data)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteRow(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	tableName := r.PathValue("table")
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = "public"
	}
	pkColumn := r.URL.Query().Get("pk_column")
	pkValue := r.URL.Query().Get("pk_value")
	if pkColumn == "" || !r.URL.Query().Has("pk_value") {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "pk_column and pk_value query params required"})
		return
	}

	status, err := s.tableService.DeleteRow(r.Context(), projectID, schema, tableName, pkColumn, pkValue)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

// ---------- SQL editor handler ----------

func (s *Server) handleExecuteSQL(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "developer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	var req platform.SQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	result, status, err := s.sqlService.ExecuteSQL(r.Context(), projectID, req)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

// ---------- Auth user management handlers ----------

func (s *Server) handleListAuthUsers(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	search := r.URL.Query().Get("search")

	result, status, err := s.authUserService.ListAuthUsers(r.Context(), projectID, page, perPage, search)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleGetAuthUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	authUserID := r.PathValue("uid")
	if !isValidUUID(projectID) || !isValidUUID(authUserID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	result, status, err := s.authUserService.GetAuthUser(r.Context(), projectID, authUserID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, result)
}

func (s *Server) handleDeleteAuthUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	authUserID := r.PathValue("uid")
	if !isValidUUID(projectID) || !isValidUUID(authUserID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	status, err := s.authUserService.DeleteAuthUser(r.Context(), projectID, authUserID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "deleted"})
}

func (s *Server) handleBanAuthUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	authUserID := r.PathValue("uid")
	if !isValidUUID(projectID) || !isValidUUID(authUserID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	status, err := s.authUserService.BanAuthUser(r.Context(), projectID, authUserID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "banned"})
}

func (s *Server) handleUnbanAuthUser(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	authUserID := r.PathValue("uid")
	if !isValidUUID(projectID) || !isValidUUID(authUserID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "admin") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	status, err := s.authUserService.UnbanAuthUser(r.Context(), projectID, authUserID)
	if err != nil {
		dupaHTTP.WriteJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	dupaHTTP.WriteJSON(w, status, map[string]string{"status": "unbanned"})
}

// ---------- Logs handler ----------

func (s *Server) handleProjectLogs(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	projectID := r.PathValue("id")
	if !isValidUUID(projectID) {
		dupaHTTP.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project ID format"})
		return
	}

	orgID, err := s.getProjectOrgID(r.Context(), projectID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}
	role, err := s.orgService.CheckOrgRole(r.Context(), orgID, userID)
	if err != nil {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not a member of this organization"})
		return
	}
	if !platform.HasMinRole(role, "viewer") {
		dupaHTTP.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage := 50
	offset := (page - 1) * perPage

	action := r.URL.Query().Get("action")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	// Build query dynamically
	query := `SELECT id, user_id, action, resource_type, resource_id, ip_address, user_agent, metadata, created_at
		FROM platform.audit_log WHERE resource_id = $1`
	args := []interface{}{projectID}
	argIdx := 2

	if action != "" {
		query += fmt.Sprintf(` AND action = $%d`, argIdx)
		args = append(args, action)
		argIdx++
	}
	if from != "" {
		query += fmt.Sprintf(` AND created_at >= $%d`, argIdx)
		args = append(args, from)
		argIdx++
	}
	if to != "" {
		query += fmt.Sprintf(` AND created_at <= $%d`, argIdx)
		args = append(args, to)
		argIdx++
	}

	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
	args = append(args, perPage, offset)

	rows, err := s.platformDB.Query(r.Context(), query, args...)
	if err != nil {
		slog.Error("failed to query logs", "error", err)
		dupaHTTP.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to query logs"})
		return
	}
	defer rows.Close()

	type LogEntry struct {
		ID           int64       `json:"id"`
		UserID       *string     `json:"user_id"`
		Action       string      `json:"action"`
		ResourceType *string     `json:"resource_type"`
		ResourceID   *string     `json:"resource_id"`
		IPAddress    *string     `json:"ip_address"`
		UserAgent    *string     `json:"user_agent"`
		Metadata     interface{} `json:"metadata"`
		CreatedAt    string      `json:"created_at"`
	}

	var logs []LogEntry
	for rows.Next() {
		var l LogEntry
		if err := rows.Scan(&l.ID, &l.UserID, &l.Action, &l.ResourceType, &l.ResourceID,
			&l.IPAddress, &l.UserAgent, &l.Metadata, &l.CreatedAt); err != nil {
			continue
		}
		logs = append(logs, l)
	}

	if logs == nil {
		logs = []LogEntry{}
	}

	dupaHTTP.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"logs": logs,
		"page": page,
	})
}

// isValidUUID validates a UUID string format (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx).
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// ---------- Helpers ----------

// getProjectOrgID looks up the org_id for a project.
// resolveBackupOrgID resolves the org_id for backup endpoints.
// If orgID is provided in query/body, use it; otherwise fall back to personal org.
func (s *Server) resolveBackupOrgID(r *http.Request, userID, orgID string) (string, error) {
	if orgID != "" {
		if !isValidUUID(orgID) {
			return "", fmt.Errorf("invalid org_id format")
		}
		return orgID, nil
	}
	return s.orgService.GetPersonalOrgID(r.Context(), userID)
}

func (s *Server) getProjectOrgID(ctx context.Context, projectID string) (string, error) {
	var orgID string
	err := s.platformDB.QueryRow(ctx, `
		SELECT org_id FROM platform.projects WHERE id = $1 AND status != 'deleted'
	`, projectID).Scan(&orgID)
	return orgID, err
}

// allowedOrigins returns the list of origins permitted for CORS.
// Set ALLOWED_ORIGINS env var to a comma-separated list of origins that should
// receive Access-Control-Allow-Credentials: true. If unset, all origins get
// the wildcard "*" response (no credentials).
func allowedOrigins() map[string]bool {
	origins := map[string]bool{}
	originsStr := os.Getenv("ALLOWED_ORIGINS")
	if originsStr != "" {
		for _, o := range strings.Split(originsStr, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				origins[o] = true
			}
		}
	}
	return origins
}

var corsOrigins = allowedOrigins()

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" && corsOrigins[origin] {
			// Whitelisted origin — reflect it back with credentials
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			// No origin or unknown origin — use wildcard, no credentials
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}

		w.Header().Set("Vary", "Origin")

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")

		// Reflect whatever headers the client requests — Dupabase shouldn't
		// need to know about every custom header upstream apps decide to send.
		if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "*")
		}

		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, X-Total-Count")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
