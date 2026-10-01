"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useCallback, useEffect, useState } from "react";
import { useAuth } from "@/lib/auth-context";
import { imports, type ImportTask } from "@/lib/api";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Loader2, FileUp, Clock } from "lucide-react";

interface ImportHistoryProps {
  projectId: string;
  refreshKey?: number;
}

const STATUS_CONFIG: Record<string, { label: string; variant: "default" | "secondary" | "destructive" | "outline" }> = {
  uploading: { label: "Uploading", variant: "outline" },
  running: { label: "Running", variant: "default" },
  completed: { label: "Completed", variant: "secondary" },
  failed: { label: "Failed", variant: "destructive" },
  cancelled: { label: "Cancelled", variant: "outline" },
};

export function ImportHistory({ projectId, refreshKey }: ImportHistoryProps) {
  const { token } = useAuth();
  const [tasks, setTasks] = useState<ImportTask[]>([]);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    if (!token) return;
    const { data } = await imports.history(token, projectId);
    if (data) setTasks(data);
    setLoading(false);
  }, [token, projectId]);

  useEffect(() => deferEffect(() => {
    load();
  }), [load, refreshKey]);

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  };

  const formatDate = (dateStr: string) => {
    const d = new Date(dateStr);
    return d.toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  };

  const formatDuration = (start: string, end: string | null) => {
    if (!end) return "—";
    const ms = new Date(end).getTime() - new Date(start).getTime();
    if (ms < 1000) return "<1s";
    if (ms < 60000) return `${Math.round(ms / 1000)}s`;
    return `${Math.round(ms / 60000)}m ${Math.round((ms % 60000) / 1000)}s`;
  };

  if (loading) {
    return (
      <Card>
        <CardContent className="flex items-center justify-center py-8">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Import History</CardTitle>
        <CardDescription>Previous database imports for this project.</CardDescription>
      </CardHeader>
      <CardContent>
        {tasks.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-8 text-muted-foreground">
            <FileUp className="h-8 w-8" />
            <p className="text-sm">No imports yet</p>
          </div>
        ) : (
          <div className="space-y-3">
            {tasks.map((task) => {
              const config = STATUS_CONFIG[task.status] || STATUS_CONFIG.cancelled;
              return (
                <div
                  key={task.id}
                  className="flex items-start sm:items-center gap-3 rounded-lg border p-3"
                >
                  <FileUp className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5 sm:mt-0" />
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-medium truncate">
                      {task.file_name}
                    </p>
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
                      <span>{formatSize(task.file_size)}</span>
                      <span className="text-muted-foreground/50">|</span>
                      <span>{task.format.toUpperCase()}</span>
                      {task.tables_imported != null && (
                        <>
                          <span className="text-muted-foreground/50">|</span>
                          <span>{task.tables_imported} tables</span>
                        </>
                      )}
                    </div>
                  </div>
                  <div className="shrink-0 text-right">
                    <Badge variant={config.variant} className="text-xs">
                      {task.status === "running" && (
                        <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                      )}
                      {config.label}
                    </Badge>
                    <div className="flex items-center gap-1 text-xs text-muted-foreground mt-1 justify-end">
                      <Clock className="h-3 w-3" />
                      <span>{formatDate(task.started_at)}</span>
                      {task.completed_at && (
                        <span className="text-muted-foreground/50">
                          ({formatDuration(task.started_at, task.completed_at)})
                        </span>
                      )}
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
