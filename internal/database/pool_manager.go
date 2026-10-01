package database

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/ansoraGROUP/dupabase/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

type poolEntry struct {
	pool     *pgxpool.Pool
	dbName   string
	lastUsed time.Time
}

type projectCacheEntry struct {
	project  ProjectRecord
	cachedAt time.Time
}

// ProjectRecord is the minimal project info needed for routing.
type ProjectRecord struct {
	ID             string
	DBName         string
	JWTSecret      string
	AnonKey        string
	ServiceRoleKey string
	EnableSignup   bool
	Autoconfirm    bool
	PasswordMinLen int
	SiteURL        string
	Status         string
}

type PoolManager struct {
	mu           sync.RWMutex
	pools        map[string]*poolEntry
	projectCache map[string]*projectCacheEntry
	platformPool *pgxpool.Pool
	baseURL      *url.URL // parsed DATABASE_URL for building per-project URLs
	cfg          *config.Config
	stopCh       chan struct{}
}

func NewPoolManager(cfg *config.Config, platformPool *pgxpool.Pool) (*PoolManager, error) {
	parsed, err := url.Parse(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	pm := &PoolManager{
		pools:        make(map[string]*poolEntry),
		projectCache: make(map[string]*projectCacheEntry),
		platformPool: platformPool,
		baseURL:      parsed,
		cfg:          cfg,
		stopCh:       make(chan struct{}),
	}

	// Start idle pool eviction goroutine
	go pm.evictionLoop()

	return pm, nil
}

func (pm *PoolManager) PlatformPool() *pgxpool.Pool {
	return pm.platformPool
}

// Config returns the startup defaults used for provisioning new projects.
func (pm *PoolManager) Config() *config.Config {
	return pm.cfg
}

// GetProject looks up a project by ID with caching.
func (pm *PoolManager) GetProject(ctx context.Context, projectID string) (*ProjectRecord, error) {
	pm.mu.RLock()
	cached, ok := pm.projectCache[projectID]
	pm.mu.RUnlock()

	if ok && time.Since(cached.cachedAt) < 60*time.Second {
		return &cached.project, nil
	}

	row := pm.platformPool.QueryRow(ctx, `
		SELECT id, db_name, jwt_secret, anon_key, service_role_key,
		       enable_signup, autoconfirm, password_min_length, site_url, status
		FROM platform.projects
		WHERE id = $1 AND status = 'active'
	`, projectID)

	var p ProjectRecord
	err := row.Scan(&p.ID, &p.DBName, &p.JWTSecret, &p.AnonKey, &p.ServiceRoleKey,
		&p.EnableSignup, &p.Autoconfirm, &p.PasswordMinLen, &p.SiteURL, &p.Status)
	if err != nil {
		return nil, err
	}

	pm.mu.Lock()
	pm.projectCache[projectID] = &projectCacheEntry{project: p, cachedAt: time.Now()}
	pm.mu.Unlock()

	return &p, nil
}

// InvalidateProjectCache removes a project from the cache.
func (pm *PoolManager) InvalidateProjectCache(projectID string) {
	pm.mu.Lock()
	delete(pm.projectCache, projectID)
	pm.mu.Unlock()
}

// GetPool returns or creates a connection pool for a project's database.
func (pm *PoolManager) GetPool(ctx context.Context, projectID string) (*pgxpool.Pool, error) {
	pm.mu.RLock()
	entry, exists := pm.pools[projectID]
	pm.mu.RUnlock()

	if exists {
		pm.mu.Lock()
		entry.lastUsed = time.Now()
		pm.mu.Unlock()
		return entry.pool, nil
	}

	// Look up the project
	project, err := pm.GetProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project %s: %w", projectID, err)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists := pm.pools[projectID]; exists {
		entry.lastUsed = time.Now()
		return entry.pool, nil
	}

	// Evict LRU if at global limit
	if len(pm.pools)*int(pm.cfg.MaxConnectionsPerDB) >= pm.cfg.GlobalMaxConnections {
		pm.evictLRULocked()
	}

	// Provisioning uses the operator identity; all tenant traffic uses a real
	// project login. SET ROLE on an operator connection is not an isolation boundary.
	var owner string
	if err := pm.platformPool.QueryRow(ctx, `SELECT u.pg_username FROM platform.pg_users u
		JOIN platform.projects p ON p.pg_user_id = u.id WHERE p.id = $1`, projectID).Scan(&owner); err != nil {
		return nil, fmt.Errorf("get project database owner: %w", err)
	}
	if err := pm.ProvisionProjectLogin(ctx, projectID, project.DBName, owner); err != nil {
		return nil, err
	}
	dbURL := pm.projectURL(projectID, project.DBName)
	poolCfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse DB URL for %s: %w", project.DBName, err)
	}
	poolCfg.MaxConns = int32(pm.cfg.MaxConnectionsPerDB)
	poolCfg.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool for %s: %w", project.DBName, err)
	}

	pm.pools[projectID] = &poolEntry{
		pool:     pool,
		dbName:   project.DBName,
		lastUsed: time.Now(),
	}

	return pool, nil
}

// ClosePool closes a specific project's pool.
func (pm *PoolManager) ClosePool(projectID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if entry, ok := pm.pools[projectID]; ok {
		entry.pool.Close()
		delete(pm.pools, projectID)
		delete(pm.projectCache, projectID)
	}
}

// Shutdown closes all pools gracefully.
func (pm *PoolManager) Shutdown() {
	close(pm.stopCh)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, entry := range pm.pools {
		entry.pool.Close()
	}
	pm.pools = make(map[string]*poolEntry)
	pm.projectCache = make(map[string]*projectCacheEntry)
}

func (pm *PoolManager) buildDBURL(dbName string) string {
	u := *pm.baseURL
	u.Path, u.RawPath = "/"+dbName, ""
	q := u.Query()
	q.Del("dbname")
	q.Del("database")
	u.RawQuery = q.Encode()
	return u.String()
}

func (pm *PoolManager) evictionLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.stopCh:
			return
		case <-ticker.C:
			pm.evictIdle()
		}
	}
}

func (pm *PoolManager) evictIdle() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	timeout := time.Duration(pm.cfg.PoolIdleTimeout) * time.Second
	for id, entry := range pm.pools {
		if time.Since(entry.lastUsed) > timeout {
			entry.pool.Close()
			delete(pm.pools, id)
			delete(pm.projectCache, id)
		}
	}
}

func (pm *PoolManager) evictLRULocked() {
	var oldestID string
	var oldestTime time.Time

	for id, entry := range pm.pools {
		if oldestID == "" || entry.lastUsed.Before(oldestTime) {
			oldestID = id
			oldestTime = entry.lastUsed
		}
	}

	if oldestID != "" {
		pm.pools[oldestID].pool.Close()
		delete(pm.pools, oldestID)
		delete(pm.projectCache, oldestID)
	}
}
