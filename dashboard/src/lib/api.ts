const query = (values: Record<string, string | number>) => `?${new URLSearchParams(Object.entries(values).map(([key, value]) => [key, String(value)]))}`;
const segment = (value: string | number) => encodeURIComponent(String(value));

const API_URL = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:3333";

interface ApiOptions {
  method?: string;
  body?: unknown;
  token?: string;
}

export async function api<T = unknown>(
  path: string,
  opts: ApiOptions = {}
): Promise<{ data: T | null; error: string | null }> {
  const { method = "GET", body, token } = opts;
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (token) headers["Authorization"] = `Bearer ${token}`;

  try {
    const res = await fetch(`${API_URL}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    const json = await res.json();
    if (!res.ok) {
      return { data: null, error: json.error || json.message || `Error ${res.status}` };
    }
    return { data: json as T, error: null };
  } catch (err) {
    return { data: null, error: (err as Error).message };
  }
}

// Platform Auth
export const platformAuth = {
  register: (email: string, password: string, inviteCode?: string) =>
    api<{ user: PlatformUser; token: string }>("/platform/auth/register", {
      method: "POST",
      body: { email, password, ...(inviteCode ? { invite_code: inviteCode } : {}) },
    }),
  login: (email: string, password: string) =>
    api<{ user: PlatformUser; token: string }>("/platform/auth/login", {
      method: "POST",
      body: { email, password },
    }),
  me: (token: string) =>
    api<PlatformUser>("/platform/auth/me", { token }),
  changePassword: (token: string, currentPassword: string, newPassword: string) =>
    api<{ message: string }>("/platform/auth/password", {
      method: "PUT",
      body: { current_password: currentPassword, new_password: newPassword },
      token,
    }),
};

// Projects
export const projects = {
  list: (token: string, orgId?: string) =>
    api<Project[]>(`/platform/projects${orgId ? query({org_id: orgId}) : ""}`, { token }),
  get: (token: string, id: string) =>
    api<Project>(`/platform/projects/${segment(id)}`, { token }),
  create: (token: string, name: string, orgId?: string) =>
    api<Project>("/platform/projects", {
      method: "POST",
      body: { name, ...(orgId ? { org_id: orgId } : {}) },
      token,
    }),
  delete: (token: string, id: string) =>
    api<{ message: string }>(`/platform/projects/${segment(id)}`, {
      method: "DELETE",
      token,
    }),
  updateSettings: (token: string, id: string, settings: Partial<ProjectSettings>) =>
    api<ProjectSettings>(`/platform/projects/${segment(id)}/settings`, {
      method: "PATCH",
      body: settings,
      token,
    }),
};

// Credentials
export const credentials = {
  reveal: (token: string, password: string) =>
    api<{ pg_username: string; pg_password: string }>(
      "/platform/credentials/reveal",
      { method: "POST", body: { platform_password: password }, token }
    ),
};

// Import
export const imports = {
  start: async (
    token: string,
    projectId: string,
    file: File,
    options: ImportOptions = {}
  ): Promise<{ data: ImportTask | null; error: string | null }> => {
    const formData = new FormData();
    formData.append("file", file);
    if (options.clean_import) formData.append("clean_import", "true");
    formData.append("skip_auth_schema", options.skip_auth_schema === false ? "false" : "true");
    formData.append("disable_triggers", options.disable_triggers === false ? "false" : "true");
    if (options.migrate_auth_users) formData.append("migrate_auth_users", "true");
    try {
      const res = await fetch(
        `${API_URL}/platform/projects/${segment(projectId)}/import`,
        {
          method: "POST",
          headers: { Authorization: `Bearer ${token}` },
          body: formData,
        }
      );
      const json = await res.json();
      if (!res.ok) return { data: null, error: json.error || `Error ${res.status}` };
      return { data: json as ImportTask, error: null };
    } catch (err) {
      return { data: null, error: (err as Error).message };
    }
  },
  status: (token: string, projectId: string, taskId: number) =>
    api<ImportTask>(`/platform/projects/${segment(projectId)}/import/${segment(taskId)}`, { token }),
  history: (token: string, projectId: string) =>
    api<ImportTask[]>(`/platform/projects/${segment(projectId)}/import/history`, { token }),
  cancel: (token: string, projectId: string, taskId: number) =>
    api<{ status: string }>(`/platform/projects/${segment(projectId)}/import/${segment(taskId)}/cancel`, {
      method: "POST",
      token,
    }),
  analyze: async (token: string, projectId: string, file: File) => {
    const formData = new FormData();
    formData.append("file", file);
    try {
      const res = await fetch(`${API_URL}/platform/projects/${segment(projectId)}/import/analyze`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
        body: formData,
      });
      const json = await res.json();
      if (!res.ok) return { data: null, error: json.error || `Error ${res.status}` };
      return { data: json as DumpAnalysis, error: null };
    } catch (err) {
      return { data: null, error: (err as Error).message };
    }
  },
};

// Backups
export const backups = {
  getSettings: (token: string, orgId?: string) =>
    api<BackupSettings>(`/platform/backups/settings${orgId ? query({org_id: orgId}) : ""}`, { token }),
  saveSettings: (token: string, settings: SaveBackupSettingsRequest) =>
    api<BackupSettings>("/platform/backups/settings", {
      method: "POST",
      body: settings,
      token,
    }),
  getHistory: (token: string, orgId?: string) =>
    api<BackupHistoryItem[]>(`/platform/backups/history${orgId ? query({org_id: orgId}) : ""}`, { token }),
  runNow: (token: string, orgId?: string) =>
    api<{ status: string }>(`/platform/backups/run${orgId ? query({org_id: orgId}) : ""}`, {
      method: "POST",
      token,
    }),
  testConnection: (token: string, config: TestS3ConnectionRequest) =>
    api<{ status: string }>("/platform/backups/test-connection", {
      method: "POST",
      body: config,
      token,
    }),
  toggleEnabled: (token: string, enabled: boolean, platformPassword: string, orgId?: string) =>
    api<BackupSettings>("/platform/backups/settings", {
      method: "PATCH",
      body: { enabled, platform_password: platformPassword, ...(orgId ? {org_id: orgId} : {}) },
      token,
    }),
  restoreBackup: (token: string, historyId: number, platformPassword: string) =>
    api<{ task_id: number; status: string }>(`/platform/backups/${segment(historyId)}/restore`, {
      method: "POST",
      body: { platform_password: platformPassword },
      token,
    }),
};

// Registration mode (public, no auth)
export const registrationMode = {
  get: () => api<{ registration_mode: string }>("/platform/auth/registration-mode"),
};

// Admin
export const admin = {
  getUsers: (token: string, page = 1, perPage = 20) =>
    api<PaginatedUsers>(`/platform/admin/users${query({page, per_page: perPage})}`, { token }),
  deleteUser: (token: string, id: string) =>
    api<{ status: string }>(`/platform/admin/users/${segment(id)}`, {
      method: "DELETE",
      token,
    }),
  getSettings: (token: string) =>
    api<AdminPlatformSettings>("/platform/admin/settings", { token }),
  updateSettings: (token: string, settings: AdminPlatformSettings) =>
    api<{ status: string }>("/platform/admin/settings", {
      method: "PUT",
      body: settings,
      token,
    }),
  getInvites: (token: string) =>
    api<AdminInvite[]>("/platform/admin/invites", { token }),
  createInvite: (token: string, email?: string, expiresInHours?: number) =>
    api<AdminInvite>("/platform/admin/invites", {
      method: "POST",
      body: { email, expires_in_hours: expiresInHours },
      token,
    }),
  deleteInvite: (token: string, id: string) =>
    api<{ status: string }>(`/platform/admin/invites/${segment(id)}`, {
      method: "DELETE",
      token,
    }),
};

// Organizations
export const orgs = {
  create: (token: string, name: string, slug: string) =>
    api<Organization>("/platform/orgs", {
      method: "POST",
      body: { name, slug },
      token,
    }),
  list: (token: string) =>
    api<Organization[]>("/platform/orgs", { token }),
  get: (token: string, id: string) =>
    api<OrgDetail>(`/platform/orgs/${segment(id)}`, { token }),
  update: (token: string, id: string, data: { name?: string; slug?: string }) =>
    api<Organization>(`/platform/orgs/${segment(id)}`, {
      method: "PATCH",
      body: data,
      token,
    }),
  delete: (token: string, id: string) =>
    api<{ status: string }>(`/platform/orgs/${segment(id)}`, {
      method: "DELETE",
      token,
    }),
  listMembers: (token: string, id: string) =>
    api<OrgMember[]>(`/platform/orgs/${segment(id)}/members`, { token }),
  createInvite: (token: string, id: string, email: string, role: string) =>
    api<OrgInvite>(`/platform/orgs/${segment(id)}/invites`, {
      method: "POST",
      body: { email, role },
      token,
    }),
  acceptInvite: (token: string, inviteToken: string) =>
    api<Organization>(`/platform/orgs/invites/${segment(inviteToken)}/accept`, {
      method: "POST",
      token,
    }),
  removeMember: (token: string, id: string, userId: string) =>
    api<{ status: string }>(`/platform/orgs/${segment(id)}/members/${segment(userId)}`, {
      method: "DELETE",
      token,
    }),
  updateMemberRole: (token: string, id: string, userId: string, role: string) =>
    api<{ status: string }>(`/platform/orgs/${segment(id)}/members/${segment(userId)}`, {
      method: "PATCH",
      body: { role },
      token,
    }),
  listInvites: (token: string, id: string) =>
    api<OrgInvite[]>(`/platform/orgs/${segment(id)}/invites`, { token }),
  revokeInvite: (token: string, id: string, inviteId: string) =>
    api<{ status: string }>(`/platform/orgs/${segment(id)}/invites/${segment(inviteId)}`, {
      method: "DELETE",
      token,
    }),
};

// Types
export interface SaveBackupSettingsRequest {
  s3_endpoint: string;
  s3_region: string;
  s3_bucket: string;
  s3_access_key: string;
  s3_secret_key: string;
  s3_path_prefix?: string;
  schedule?: string;
  retention_days?: number;
  project_ids?: string[];
  platform_password: string;
  org_id?: string;
}

export interface TestS3ConnectionRequest {
  s3_endpoint: string;
  s3_region: string;
  s3_bucket: string;
  s3_access_key: string;
  s3_secret_key: string;
}

export interface BackupSettings {
  id: string;
  s3_endpoint: string;
  s3_region: string;
  s3_bucket: string;
  s3_path_prefix: string;
  schedule: string;
  retention_days: number;
  project_ids: string[];
  enabled: boolean;
}

export interface BackupHistoryItem {
  id: number;
  project_id: string;
  db_name: string;
  s3_key: string;
  size_bytes: number | null;
  status: "running" | "completed" | "failed" | "cancelled";
  error_message: string | null;
  started_at: string;
  completed_at: string | null;
}

export interface ImportOptions {
  clean_import?: boolean;
  skip_auth_schema?: boolean;
  disable_triggers?: boolean;
  migrate_auth_users?: boolean;
}

export interface DumpAnalysis {
  is_supabase_dump: boolean;
  format: string;
  has_auth_users: boolean;
  has_migrations: boolean;
  supabase_schemas: string[];
  detected_signals: string[];
  recommended_action: string;
  file_name: string;
  file_size: number;
}

export interface ImportTask {
  id: number;
  project_id: string;
  db_name: string;
  file_name: string;
  file_size: number;
  format: string;
  status: "uploading" | "running" | "completed" | "failed" | "cancelled";
  error_message: string | null;
  tables_imported: number | null;
  started_at: string;
  completed_at: string | null;
}

export interface PlatformUser {
  id: string;
  email: string;
  display_name?: string | null;
  pg_username: string;
  is_admin: boolean;
  created_at: string;
}

// Admin types
export interface AdminUser {
  id: string;
  email: string;
  display_name?: string | null;
  pg_username: string;
  is_admin: boolean;
  project_count: number;
  created_at: string;
}

export interface PaginatedUsers {
  users: AdminUser[];
  total: number;
  page: number;
  per_page: number;
}

export interface AdminPlatformSettings {
  registration_mode: "open" | "invite" | "disabled";
}

export interface AdminInvite {
  id: string;
  code: string;
  email: string | null;
  created_by: string;
  used_by: string | null;
  used_at: string | null;
  expires_at: string;
  created_at: string;
}

export interface ProjectSettings {
  enable_signup: boolean;
  autoconfirm: boolean;
  password_min_length: number;
}

export interface Project {
  id: string;
  name: string;
  db_name: string;
  anon_key: string;
  service_role_key: string;
  jwt_secret: string;
  site_url: string;
  settings: ProjectSettings;
  created_at: string;
}

// Organization types
export interface Organization {
  id: string;
  name: string;
  slug: string;
  created_by: string;
  role?: string;
  created_at: string;
  updated_at: string;
}

export interface OrgDetail extends Organization {
  member_count: number;
}

export interface OrgMember {
  id: string;
  org_id: string;
  user_id: string;
  email: string;
  role: 'owner' | 'admin' | 'developer' | 'viewer';
  created_at: string;
}

export interface OrgInvite {
  id: string;
  org_id: string;
  email: string;
  role: 'owner' | 'admin' | 'developer' | 'viewer';
  invited_by: string;
  token: string;
  accepted_at: string | null;
  expires_at: string;
  created_at: string;
}

// Table browser types
export interface TableInfo {
  schema: string;
  name: string;
  column_count: number;
}

export interface ColumnInfo {
  primary_key?: boolean;
  unique?: boolean;
  name: string;
  type: string;
  nullable: boolean;
  default: string | null;
  max_length: number | null;
  precision: number | null;
}

export interface TableRowsResponse {
  columns: string[];
  rows: unknown[][];
  total: number;
  page: number;
  per_page: number;
}

// SQL editor types
export interface SQLRequest {
  query: string;
  read_only?: boolean;
}

export interface SQLResponse {
  columns: string[];
  rows: unknown[][];
  row_count: number;
  execution_time_ms: number;
}

// Auth user types
export interface AuthUserInfo {
  id: string;
  email: string | null;
  phone: string | null;
  email_confirmed_at: string | null;
  phone_confirmed_at: string | null;
  last_sign_in_at: string | null;
  is_anonymous: boolean;
  banned_until: string | null;
  created_at: string;
  updated_at: string;
}

export interface AuthUserDetail extends AuthUserInfo {
  app_metadata: unknown;
  user_metadata: unknown;
  sessions: AuthSessionInfo[];
}

export interface AuthSessionInfo {
  id: string;
  created_at: string;
  updated_at: string;
  user_agent: string | null;
  ip: string | null;
}

export interface AuthUserListResponse {
  users: AuthUserInfo[];
  total: number;
  page: number;
  per_page: number;
}

// Log types
export interface LogEntry {
  id: number;
  user_id: string | null;
  action: string;
  resource_type: string | null;
  resource_id: string | null;
  ip_address: string | null;
  user_agent: string | null;
  metadata: unknown;
  created_at: string;
}

// Analytics types
export interface TableStats {
  schema: string;
  name: string;
  row_count: number;
  total_size: number;
  index_size: number;
}

export interface DatabaseAnalytics {
  db_size: number;
  table_count: number;
  total_rows: number;
  tables: TableStats[];
}

export interface ConnectionState {
  state: string;
  count: number;
}

export interface ConnectionAnalytics {
  total: number;
  active: number;
  idle: number;
  idle_in_transaction: number;
  connections: ConnectionState[];
}

export interface QueryStats {
  query: string;
  calls: number;
  total_time_ms: number;
  mean_time_ms: number;
  rows: number;
}

export interface QueryAnalytics {
  available: boolean;
  queries: QueryStats[];
}

export interface AuthAnalytics {
  total_users: number;
  signups_7d: number;
  signups_30d: number;
  active_sessions: number;
}

export interface DailyUsage {
  day: string;
  action: string;
  count: number;
}

export interface APIUsageAnalytics {
  daily_usage: DailyUsage[];
}

export interface OverviewAnalytics {
  database: DatabaseAnalytics | null;
  connections: ConnectionAnalytics | null;
  auth: AuthAnalytics | null;
  api_usage: APIUsageAnalytics | null;
}

// Tables
export const tables = {
  list: (token: string, projectId: string) =>
    api<TableInfo[]>(`/platform/projects/${segment(projectId)}/tables`, { token }),
  columns: (token: string, projectId: string, table: string, schema = "public") =>
    api<ColumnInfo[]>(`/platform/projects/${segment(projectId)}/tables/${segment(table)}/columns${query({schema})}`, { token }),
  rows: (token: string, projectId: string, table: string, params: { schema?: string; page?: number; perPage?: number; orderBy?: string; orderDir?: string }) =>
    api<TableRowsResponse>(`/platform/projects/${segment(projectId)}/tables/${segment(table)}/rows${query({schema: params.schema || "public", page: params.page || 1, per_page: params.perPage || 50, order_by: params.orderBy || "", order_dir: params.orderDir || ""})}`, { token }),
  insertRow: (token: string, projectId: string, table: string, data: Record<string, unknown>, schema = "public") =>
    api<unknown>(`/platform/projects/${segment(projectId)}/tables/${segment(table)}/rows${query({schema})}`, { method: "POST", token, body: data }),
  updateRow: (token: string, projectId: string, table: string, pkColumn: string, pkValue: string, data: Record<string, unknown>, schema = "public") =>
    api<unknown>(`/platform/projects/${segment(projectId)}/tables/${segment(table)}/rows${query({schema, pk_column: pkColumn, pk_value: pkValue})}`, { method: "PATCH", token, body: data }),
  deleteRow: (token: string, projectId: string, table: string, pkColumn: string, pkValue: string, schema = "public") =>
    api<unknown>(`/platform/projects/${segment(projectId)}/tables/${segment(table)}/rows${query({schema, pk_column: pkColumn, pk_value: pkValue})}`, { method: "DELETE", token }),
};

// SQL
export const sql = {
  execute: (token: string, projectId: string, query: string, readOnly = false) =>
    api<SQLResponse>(`/platform/projects/${segment(projectId)}/sql`, { method: "POST", token, body: { query, read_only: readOnly } }),
};

// Auth Users
export const authUsers = {
  list: (token: string, projectId: string, params?: { page?: number; perPage?: number; search?: string }) =>
    api<AuthUserListResponse>(`/platform/projects/${segment(projectId)}/auth/users${query({page: params?.page || 1, per_page: params?.perPage || 50, search: params?.search || ""})}`, { token }),
  get: (token: string, projectId: string, userId: string) =>
    api<AuthUserDetail>(`/platform/projects/${segment(projectId)}/auth/users/${segment(userId)}`, { token }),
  delete: (token: string, projectId: string, userId: string) =>
    api<unknown>(`/platform/projects/${segment(projectId)}/auth/users/${segment(userId)}`, { method: "DELETE", token }),
  ban: (token: string, projectId: string, userId: string) =>
    api<unknown>(`/platform/projects/${segment(projectId)}/auth/users/${segment(userId)}/ban`, { method: "POST", token }),
  unban: (token: string, projectId: string, userId: string) =>
    api<unknown>(`/platform/projects/${segment(projectId)}/auth/users/${segment(userId)}/unban`, { method: "POST", token }),
};

// Logs
export const logs = {
  list: (token: string, projectId: string, params?: { page?: number; action?: string; from?: string; to?: string }) =>
    api<{ logs: LogEntry[]; page: number }>(`/platform/projects/${segment(projectId)}/logs${query({page: params?.page || 1, action: params?.action || "", from: params?.from || "", to: params?.to || ""})}`, { token }),
};

// Analytics
export const analytics = {
  overview: (token: string, projectId: string) =>
    api<OverviewAnalytics>(`/platform/projects/${segment(projectId)}/analytics/overview`, { token }),
  database: (token: string, projectId: string) =>
    api<DatabaseAnalytics>(`/platform/projects/${segment(projectId)}/analytics/database`, { token }),
  connections: (token: string, projectId: string) =>
    api<ConnectionAnalytics>(`/platform/projects/${segment(projectId)}/analytics/connections`, { token }),
  queries: (token: string, projectId: string) =>
    api<QueryAnalytics>(`/platform/projects/${segment(projectId)}/analytics/queries`, { token }),
  auth: (token: string, projectId: string) =>
    api<AuthAnalytics>(`/platform/projects/${segment(projectId)}/analytics/auth`, { token }),
  apiUsage: (token: string, projectId: string) =>
    api<APIUsageAnalytics>(`/platform/projects/${segment(projectId)}/analytics/api-usage`, { token }),
};
