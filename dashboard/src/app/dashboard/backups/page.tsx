"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback } from "react";
import { useAuth } from "@/lib/auth-context";
import { useOrg } from "@/lib/org-context";
import {
  backups as backupsApi,

  projects as projectsApi,
  type BackupSettings,
  type BackupHistoryItem,
  type Project,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { toast } from "sonner";
import {
  Loader2,
  Play,
  Save,
  Clock,
  CheckCircle2,
  XCircle,
  AlertCircle,
  RotateCcw,
  Plug,
} from "lucide-react";

export default function BackupsPage() {
  const { token } = useAuth();
  const { activeOrg } = useOrg();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [runningNow, setRunningNow] = useState(false);
  const [testingConnection, setTestingConnection] = useState(false);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [history, setHistory] = useState<BackupHistoryItem[]>([]);
  const [allProjects, setAllProjects] = useState<Project[]>([]);

  // Restore dialog
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
  const [restoreHistoryId, setRestoreHistoryId] = useState<number | null>(null);
  const [restorePassword, setRestorePassword] = useState("");
  const [restoring, setRestoring] = useState(false);

  // Toggle dialog
  const [toggleDialogOpen, setToggleDialogOpen] = useState(false);
  const [toggleTarget, setToggleTarget] = useState(false);
  const [togglePassword, setTogglePassword] = useState("");
  const [toggling, setToggling] = useState(false);

  // Form state
  const [s3Endpoint, setS3Endpoint] = useState("");
  const [s3Region, setS3Region] = useState("us-east-1");
  const [s3Bucket, setS3Bucket] = useState("");
  const [s3AccessKey, setS3AccessKey] = useState("");
  const [s3SecretKey, setS3SecretKey] = useState("");
  const [s3PathPrefix, setS3PathPrefix] = useState("");
  const [schedule, setSchedule] = useState("daily");
  const [retentionDays, setRetentionDays] = useState(30);
  const [selectedProjectIds, setSelectedProjectIds] = useState<string[]>([]);
  const [platformPassword, setPlatformPassword] = useState("");

  const orgId = activeOrg?.id;

  const load = useCallback(async () => {
    if (!token) return;

    const [settingsRes, historyRes, projectsRes] = await Promise.all([
      backupsApi.getSettings(token, orgId),
      backupsApi.getHistory(token, orgId),
      projectsApi.list(token, orgId),
    ]);

    if (settingsRes.data) {
      const s = settingsRes.data;
      setSettings(s);
      setS3Endpoint(s.s3_endpoint);
      setS3Region(s.s3_region);
      setS3Bucket(s.s3_bucket);
      setS3PathPrefix(s.s3_path_prefix);
      setSchedule(s.schedule);
      setRetentionDays(s.retention_days);
      setSelectedProjectIds(s.project_ids || []);
    } else {
      setSettings(null);
    }

    if (historyRes.data) {
      setHistory(historyRes.data);
    } else {
      setHistory([]);
    }

    if (projectsRes.data) {
      setAllProjects(projectsRes.data);
    }

    setLoading(false);
  }, [token, orgId]);

  useEffect(() => deferEffect(() => {
    setLoading(true);
    load();
  }), [load]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !platformPassword) return;

    setSaving(true);
    const { data, error } = await backupsApi.saveSettings(token, {
      s3_endpoint: s3Endpoint,
      s3_region: s3Region,
      s3_bucket: s3Bucket,
      s3_access_key: s3AccessKey,
      s3_secret_key: s3SecretKey,
      s3_path_prefix: s3PathPrefix,
      schedule,
      retention_days: retentionDays,
      project_ids: selectedProjectIds,
      platform_password: platformPassword,
      org_id: orgId,
    });
    setSaving(false);

    if (error) {
      toast.error(error);
    } else if (data) {
      setSettings(data);
      setS3AccessKey("");
      setS3SecretKey("");
      setPlatformPassword("");
      toast.success("Backup settings saved");
    }
  };

  const handleRunNow = async () => {
    if (!token) return;
    setRunningNow(true);
    const { error } = await backupsApi.runNow(token, orgId);
    setRunningNow(false);

    if (error) {
      toast.error(error);
    } else {
      toast.success("Backup started");
      setTimeout(() => load(), 2000);
    }
  };

  const handleTestConnection = async () => {
    if (!token) return;
    if (!s3Endpoint || !s3Bucket || !s3AccessKey || !s3SecretKey) {
      toast.error("Fill in all S3 connection fields first");
      return;
    }
    setTestingConnection(true);
    const { error } = await backupsApi.testConnection(token, {
      s3_endpoint: s3Endpoint,
      s3_region: s3Region || "us-east-1",
      s3_bucket: s3Bucket,
      s3_access_key: s3AccessKey,
      s3_secret_key: s3SecretKey,
    });
    setTestingConnection(false);

    if (error) {
      toast.error(`Connection failed: ${error}`);
    } else {
      toast.success("S3 connection successful");
    }
  };

  const handleToggle = async () => {
    if (!token || !togglePassword) return;
    setToggling(true);
    const { data, error } = await backupsApi.toggleEnabled(token, toggleTarget, togglePassword, activeOrg?.id);
    setToggling(false);

    if (error) {
      toast.error(error);
    } else if (data) {
      setSettings(data);
      toast.success(toggleTarget ? "Backups enabled" : "Backups disabled");
      setToggleDialogOpen(false);
      setTogglePassword("");
    }
  };

  const handleRestore = async () => {
    if (!token || restoreHistoryId == null || !restorePassword) return;
    setRestoring(true);
    const { data, error } = await backupsApi.restoreBackup(token, restoreHistoryId, restorePassword);
    setRestoring(false);

    if (error) {
      toast.error(error);
    } else if (data) {
      toast.success("Restore started. Check import history for progress.");
      setRestoreDialogOpen(false);
      setRestorePassword("");
      setTimeout(() => load(), 2000);
    }
  };

  const toggleProject = (projectId: string) => {
    setSelectedProjectIds((prev) =>
      prev.includes(projectId)
        ? prev.filter((id) => id !== projectId)
        : [...prev, projectId]
    );
  };

  const selectAll = () => {
    setSelectedProjectIds([]);
  };

  const formatBytes = (bytes: number | null) => {
    if (bytes == null) return "\u2014";
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  };

  const statusIcon = (status: string) => {
    switch (status) {
      case "completed":
        return <CheckCircle2 className="h-4 w-4 text-emerald-500" />;
      case "failed":
        return <XCircle className="h-4 w-4 text-destructive" />;
      case "running":
        return <Loader2 className="h-4 w-4 animate-spin text-blue-500" />;
      default:
        return <AlertCircle className="h-4 w-4 text-muted-foreground" />;
    }
  };

  if (loading) {
    return (
      <div className="space-y-6">
        <div>
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-72 mt-2" />
        </div>
        {[1, 2, 3].map((i) => (
          <div key={i}>
            {i > 1 && <Separator className="my-6" />}
            <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
              <div className="space-y-1.5">
                <Skeleton className="h-5 w-32" />
                <Skeleton className="h-4 w-48" />
              </div>
              <Card>
                <CardContent className="pt-6 space-y-4">
                  <Skeleton className="h-10 w-full" />
                  <Skeleton className="h-10 w-full" />
                </CardContent>
              </Card>
            </div>
          </div>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-bold tracking-tight">Backups</h2>
          <p className="text-muted-foreground">
            Configure automatic S3 backups for your project databases
          </p>
        </div>
        {settings && (
          <Button
            onClick={handleRunNow}
            disabled={runningNow || !settings.enabled}
            variant="outline"
          >
            {runningNow ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Running...
              </>
            ) : (
              <>
                <Play className="mr-2 h-4 w-4" />
                Run backup now
              </>
            )}
          </Button>
        )}
      </div>

      {settings && (
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <Switch
              checked={settings.enabled}
              onCheckedChange={(checked) => {
                setToggleTarget(checked);
                setToggleDialogOpen(true);
              }}
            />
            <span className="text-sm font-medium">
              {settings.enabled ? "Enabled" : "Disabled"}
            </span>
          </div>
          <Badge variant="outline" className="gap-1.5">
            <Clock className="h-3 w-3" />
            {settings.schedule}
          </Badge>
          <Badge variant="outline">
            {settings.project_ids.length === 0
              ? "All projects"
              : `${settings.project_ids.length} project${settings.project_ids.length > 1 ? "s" : ""}`}
          </Badge>
        </div>
      )}

      <form onSubmit={handleSave}>
        {/* S3 Connection */}
        <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
          <div className="space-y-1.5">
            <h3 className="text-sm font-medium">S3 Connection</h3>
            <p className="text-sm text-muted-foreground">
              Configure your S3-compatible storage endpoint. Works with AWS S3, MinIO, Cloudflare R2, etc.
            </p>
          </div>
          <Card>
            <CardContent className="pt-6 space-y-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="s3-endpoint">Endpoint URL</Label>
                  <Input
                    id="s3-endpoint"
                    placeholder="https://s3.amazonaws.com"
                    value={s3Endpoint}
                    onChange={(e) => setS3Endpoint(e.target.value)}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="s3-region">Region</Label>
                  <Input
                    id="s3-region"
                    placeholder="us-east-1"
                    value={s3Region}
                    onChange={(e) => setS3Region(e.target.value)}
                  />
                </div>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="s3-bucket">Bucket</Label>
                  <Input
                    id="s3-bucket"
                    placeholder="my-backups"
                    value={s3Bucket}
                    onChange={(e) => setS3Bucket(e.target.value)}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="s3-prefix">Path Prefix</Label>
                  <Input
                    id="s3-prefix"
                    placeholder="dupabase/"
                    value={s3PathPrefix}
                    onChange={(e) => setS3PathPrefix(e.target.value)}
                  />
                </div>
              </div>
              <Separator />
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="s3-access">Access Key</Label>
                  <Input
                    id="s3-access"
                    type="password"
                    placeholder={settings ? "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022 (unchanged)" : "AKIA..."}
                    value={s3AccessKey}
                    onChange={(e) => setS3AccessKey(e.target.value)}
                    required={!settings}
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="s3-secret">Secret Key</Label>
                  <Input
                    id="s3-secret"
                    type="password"
                    placeholder={settings ? "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022 (unchanged)" : "Secret..."}
                    value={s3SecretKey}
                    onChange={(e) => setS3SecretKey(e.target.value)}
                    required={!settings}
                  />
                </div>
              </div>
              <div className="flex justify-end">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={handleTestConnection}
                  disabled={testingConnection}
                >
                  {testingConnection ? (
                    <>
                      <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
                      Testing...
                    </>
                  ) : (
                    <>
                      <Plug className="mr-2 h-3.5 w-3.5" />
                      Test Connection
                    </>
                  )}
                </Button>
              </div>
            </CardContent>
          </Card>
        </div>

        <Separator className="my-6" />

        {/* Schedule */}
        <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
          <div className="space-y-1.5">
            <h3 className="text-sm font-medium">Schedule</h3>
            <p className="text-sm text-muted-foreground">
              Set how often backups should run and how long to keep them.
            </p>
          </div>
          <Card>
            <CardContent className="pt-6">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label>Frequency</Label>
                  <Select value={schedule} onValueChange={setSchedule}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="hourly">Hourly</SelectItem>
                      <SelectItem value="daily">Daily</SelectItem>
                      <SelectItem value="weekly">Weekly</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="retention">Retention (days)</Label>
                  <Input
                    id="retention"
                    type="number"
                    min={1}
                    max={365}
                    className="w-24"
                    value={retentionDays}
                    onChange={(e) => setRetentionDays(parseInt(e.target.value) || 30)}
                  />
                </div>
              </div>
            </CardContent>
          </Card>
        </div>

        <Separator className="my-6" />

        {/* Project Selection */}
        <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
          <div className="space-y-1.5">
            <h3 className="text-sm font-medium">Projects</h3>
            <p className="text-sm text-muted-foreground">
              Choose which projects to include in backups. Leave empty to back up all projects.
            </p>
          </div>
          <Card>
            <CardContent className="pt-6 space-y-4">
              <div className="flex items-center justify-between">
                <p className="text-sm font-medium">
                  {selectedProjectIds.length === 0
                    ? "All projects (default)"
                    : `${selectedProjectIds.length} of ${allProjects.length} selected`}
                </p>
                {selectedProjectIds.length > 0 && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={selectAll}
                  >
                    Reset to all
                  </Button>
                )}
              </div>
              {allProjects.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4 text-center">
                  No projects yet. Create a project first.
                </p>
              ) : (
                <div className="space-y-2 max-h-60 overflow-y-auto">
                  {allProjects.map((project) => {
                    const isSelected =
                      selectedProjectIds.length === 0 ||
                      selectedProjectIds.includes(project.id);
                    return (
                      <label
                        key={project.id}
                        className="flex items-center gap-3 rounded-md border px-3 py-2.5 cursor-pointer hover:bg-muted/50 transition-colors"
                      >
                        <Checkbox
                          checked={isSelected}
                          onCheckedChange={() => {
                            if (selectedProjectIds.length === 0) {
                              setSelectedProjectIds(
                                allProjects
                                  .filter((p) => p.id !== project.id)
                                  .map((p) => p.id)
                              );
                            } else {
                              toggleProject(project.id);
                            }
                          }}
                        />
                        <div className="flex-1 min-w-0">
                          <p className="text-sm font-medium truncate">
                            {project.name}
                          </p>
                          <p className="text-xs text-muted-foreground font-mono">
                            {project.db_name}
                          </p>
                        </div>
                        <Badge variant="secondary" className="text-xs gap-1 shrink-0">
                          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                          Active
                        </Badge>
                      </label>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        <Separator className="my-6" />

        {/* Confirm & Save */}
        <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
          <div className="space-y-1.5">
            <h3 className="text-sm font-medium">Confirm</h3>
            <p className="text-sm text-muted-foreground">
              Enter your platform password to encrypt and save the S3 credentials.
            </p>
          </div>
          <Card>
            <CardContent className="pt-6 space-y-4">
              <div className="space-y-2">
                <Label htmlFor="platform-pw">Platform password</Label>
                <Input
                  id="platform-pw"
                  type="password"
                  placeholder="Your platform password"
                  value={platformPassword}
                  onChange={(e) => setPlatformPassword(e.target.value)}
                  required
                />
              </div>
              <div className="flex justify-end">
                <Button type="submit" disabled={saving || !platformPassword}>
                  {saving ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Saving...
                    </>
                  ) : (
                    <>
                      <Save className="mr-2 h-4 w-4" />
                      Save backup settings
                    </>
                  )}
                </Button>
              </div>
            </CardContent>
          </Card>
        </div>
      </form>

      {/* Backup History */}
      {history.length > 0 && (
        <>
          <Separator className="my-6" />
          <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
            <div className="space-y-1.5">
              <h3 className="text-sm font-medium">History</h3>
              <p className="text-sm text-muted-foreground">
                Recent backup runs and their status.
              </p>
            </div>
            <Card>
              <CardContent className="pt-6 p-0">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Status</TableHead>
                      <TableHead>Database</TableHead>
                      <TableHead>Size</TableHead>
                      <TableHead>Started</TableHead>
                      <TableHead>Duration</TableHead>
                      <TableHead></TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {history.slice(0, 20).map((item) => {
                      const duration =
                        item.completed_at && item.started_at
                          ? Math.round(
                              (new Date(item.completed_at).getTime() -
                                new Date(item.started_at).getTime()) /
                                1000
                            )
                          : null;
                      return (
                        <TableRow key={item.id}>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              {statusIcon(item.status)}
                              <span className="text-xs capitalize">
                                {item.status}
                              </span>
                            </div>
                          </TableCell>
                          <TableCell>
                            <code className="text-xs font-mono">
                              {item.db_name}
                            </code>
                          </TableCell>
                          <TableCell className="text-xs">
                            {formatBytes(item.size_bytes)}
                          </TableCell>
                          <TableCell className="text-xs">
                            {new Date(item.started_at).toLocaleString()}
                          </TableCell>
                          <TableCell className="text-xs">
                            {duration != null ? `${duration}s` : "\u2014"}
                          </TableCell>
                          <TableCell>
                            {item.status === "completed" && (
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => {
                                  setRestoreHistoryId(item.id);
                                  setRestoreDialogOpen(true);
                                }}
                              >
                                <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
                                Restore
                              </Button>
                            )}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          </div>
        </>
      )}

      {/* Restore Confirmation Dialog */}
      <AlertDialog open={restoreDialogOpen} onOpenChange={setRestoreDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore Backup</AlertDialogTitle>
            <AlertDialogDescription>
              This will overwrite the current database with the backup data.
              Enter your platform password to confirm.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-2 py-2">
            <Label htmlFor="restore-pw">Platform password</Label>
            <Input
              id="restore-pw"
              type="password"
              placeholder="Your platform password"
              value={restorePassword}
              onChange={(e) => setRestorePassword(e.target.value)}
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => { setRestorePassword(""); setRestoreHistoryId(null); }}>
              Cancel
            </AlertDialogCancel>
            <Button
              onClick={handleRestore}
              disabled={restoring || !restorePassword}
              variant="destructive"
            >
              {restoring ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Restoring...
                </>
              ) : (
                "Restore"
              )}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Toggle Confirmation Dialog */}
      <AlertDialog open={toggleDialogOpen} onOpenChange={setToggleDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {toggleTarget ? "Enable Backups" : "Disable Backups"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {toggleTarget
                ? "Backups will resume on the configured schedule."
                : "Backups will stop running. Your existing backup history will be preserved."}
              {" "}Enter your platform password to confirm.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-2 py-2">
            <Label htmlFor="toggle-pw">Platform password</Label>
            <Input
              id="toggle-pw"
              type="password"
              placeholder="Your platform password"
              value={togglePassword}
              onChange={(e) => setTogglePassword(e.target.value)}
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setTogglePassword("")}>
              Cancel
            </AlertDialogCancel>
            <Button
              onClick={handleToggle}
              disabled={toggling || !togglePassword}
            >
              {toggling ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {toggleTarget ? "Enabling..." : "Disabling..."}
                </>
              ) : (
                toggleTarget ? "Enable" : "Disable"
              )}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
