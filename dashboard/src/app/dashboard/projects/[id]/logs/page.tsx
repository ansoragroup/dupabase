"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import { logs as logsApi, type LogEntry } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
  ArrowLeft,
  RefreshCw,
  ChevronLeft,
  ChevronRight,
  ScrollText,
  ChevronDown,
  ChevronUp,
  Filter,
} from "lucide-react";
import Link from "next/link";

const ACTION_OPTIONS = [
  { value: "all", label: "All Actions" },
  { value: "signup", label: "Signup" },
  { value: "login", label: "Login" },
  { value: "logout", label: "Logout" },
  { value: "token_refresh", label: "Token Refresh" },
  { value: "password_change", label: "Password Change" },
  { value: "user_update", label: "User Update" },
  { value: "user_delete", label: "User Delete" },
  { value: "project_create", label: "Project Create" },
  { value: "project_delete", label: "Project Delete" },
  { value: "project_settings_update", label: "Project Settings Update" },
  { value: "admin_user_delete", label: "Admin User Delete" },
  { value: "backup_run", label: "Backup Run" },
  { value: "import_start", label: "Import Start" },
];

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleString();
}

function getActionColor(action: string): "default" | "secondary" | "destructive" | "outline" {
  if (action.includes("delete")) return "destructive";
  if (action.includes("create") || action.includes("signup")) return "default";
  if (action.includes("login") || action.includes("token")) return "secondary";
  return "outline";
}

export default function LogsPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [logEntries, setLogEntries] = useState<LogEntry[]>([]);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [actionFilter, setActionFilter] = useState("all");
  const [fromDate, setFromDate] = useState("");
  const [toDate, setToDate] = useState("");
  const [expandedRow, setExpandedRow] = useState<number | null>(null);
  const [hasMore, setHasMore] = useState(false);

  const loadLogs = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    const { data, error } = await logsApi.list(token, id, {
      page,
      action: actionFilter === "all" ? "" : actionFilter,
      from: fromDate,
      to: toDate,
    });
    if (error) {
      toast.error(error);
    } else if (data) {
      setLogEntries(data.logs || []);
      // If we got a full page of results, there might be more
      setHasMore((data.logs || []).length >= 50);
    }
    setLoading(false);
  }, [token, id, page, actionFilter, fromDate, toDate]);

  useEffect(() => deferEffect(() => {
    loadLogs();
  }), [loadLogs]);

  const applyFilters = () => {
    setPage(1);
    loadLogs();
  };

  const toggleRow = (logId: number) => {
    setExpandedRow(expandedRow === logId ? null : logId);
  };

  if (loading && logEntries.length === 0) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-3">
          <Skeleton className="h-9 w-9 rounded-md" />
          <Skeleton className="h-7 w-48" />
        </div>
        <Card>
          <CardContent className="p-4 space-y-2">
            {[1, 2, 3, 4, 5].map((i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Button variant="ghost" size="icon" className="shrink-0" asChild>
            <Link href={`/dashboard/projects/${id}`}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
          <h2 className="text-xl sm:text-2xl font-bold tracking-tight">
            Audit Logs
          </h2>
        </div>
        <Button variant="outline" size="sm" onClick={loadLogs}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      {/* Filters */}
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-sm flex items-center gap-2">
            <Filter className="h-4 w-4" />
            Filters
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-end gap-3">
            <div className="space-y-1">
              <Label className="text-xs">Action</Label>
              <Select
                value={actionFilter}
                onValueChange={(val) => setActionFilter(val)}
              >
                <SelectTrigger className="w-[200px] h-8 text-xs">
                  <SelectValue placeholder="All Actions" />
                </SelectTrigger>
                <SelectContent>
                  {ACTION_OPTIONS.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label className="text-xs">From</Label>
              <Input
                type="datetime-local"
                className="h-8 text-xs w-[200px]"
                value={fromDate}
                onChange={(e) => setFromDate(e.target.value)}
              />
            </div>
            <div className="space-y-1">
              <Label className="text-xs">To</Label>
              <Input
                type="datetime-local"
                className="h-8 text-xs w-[200px]"
                value={toDate}
                onChange={(e) => setToDate(e.target.value)}
              />
            </div>
            <Button size="sm" variant="outline" onClick={applyFilters}>
              Apply
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Logs table */}
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Time</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Resource</TableHead>
                <TableHead>IP Address</TableHead>
                <TableHead className="hidden lg:table-cell">User Agent</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {logEntries.map((log) => (
                <>
                  <TableRow
                    key={log.id}
                    className="cursor-pointer"
                    onClick={() => toggleRow(log.id)}
                  >
                    <TableCell className="w-8 px-2">
                      {log.metadata ? (
                        expandedRow === log.id ? (
                          <ChevronUp className="h-3.5 w-3.5 text-muted-foreground" />
                        ) : (
                          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                        )
                      ) : null}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground whitespace-nowrap">
                      {formatDate(log.created_at)}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={getActionColor(log.action)}
                        className="text-[10px]"
                      >
                        {log.action}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs font-mono">
                      {log.resource_type ? (
                        <span>
                          {log.resource_type}
                          {log.resource_id && (
                            <span className="text-muted-foreground">
                              /{log.resource_id.slice(0, 8)}...
                            </span>
                          )}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">{"\u2014"}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs font-mono text-muted-foreground">
                      {log.ip_address || "\u2014"}
                    </TableCell>
                    <TableCell className="hidden lg:table-cell text-xs text-muted-foreground max-w-[200px] truncate">
                      {log.user_agent || "\u2014"}
                    </TableCell>
                  </TableRow>
                  {expandedRow === log.id && log.metadata && (
                    <TableRow key={`${log.id}-meta`}>
                      <TableCell colSpan={6} className="bg-muted/30 p-0">
                        <div className="px-4 py-3">
                          <p className="text-xs font-medium mb-2">Metadata</p>
                          <pre className="rounded-md border bg-muted/50 p-3 text-xs font-mono overflow-auto max-h-[300px]">
                            {JSON.stringify(log.metadata, null, 2)}
                          </pre>
                        </div>
                      </TableCell>
                    </TableRow>
                  )}
                </>
              ))}
              {logEntries.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={6}
                    className="text-center text-muted-foreground py-8"
                  >
                    <ScrollText className="h-8 w-8 mx-auto mb-2 opacity-50" />
                    No log entries found
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>

          {/* Pagination */}
          {(page > 1 || hasMore) && (
            <div className="flex items-center justify-between px-4 py-3 border-t">
              <p className="text-xs text-muted-foreground">Page {page}</p>
              <div className="flex items-center gap-1">
                <Button
                  variant="outline"
                  size="icon"
                  className="h-7 w-7"
                  disabled={page <= 1}
                  onClick={() => setPage(page - 1)}
                >
                  <ChevronLeft className="h-3.5 w-3.5" />
                </Button>
                <Button
                  variant="outline"
                  size="icon"
                  className="h-7 w-7"
                  disabled={!hasMore}
                  onClick={() => setPage(page + 1)}
                >
                  <ChevronRight className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
