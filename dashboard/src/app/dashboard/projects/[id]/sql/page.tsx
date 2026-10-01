"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useState, useCallback, useEffect, useRef, use } from "react";
import { useAuth } from "@/lib/auth-context";
import { sql as sqlApi, type SQLResponse } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { toast } from "sonner";
import {
  ArrowLeft,
  Play,
  Clock,
  AlertCircle,
  CheckCircle2,
  History,
  Trash2,
} from "lucide-react";
import Link from "next/link";
import Editor, { type OnMount } from "@monaco-editor/react";

const HISTORY_PREFIX = "dupabase_sql_history:";
const MAX_HISTORY = 30;

interface HistoryEntry {
  query: string;
  timestamp: number;
}

function loadHistory(key: string): HistoryEntry[] {
  try {
    const raw = sessionStorage.getItem(key);
    if (!raw) return [];
    return JSON.parse(raw) as HistoryEntry[];
  } catch {
    return [];
  }
}

function saveHistory(key: string, entries: HistoryEntry[]) {
  sessionStorage.setItem(key, JSON.stringify(entries.slice(0, MAX_HISTORY)));
}

export default function SQLEditorPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token, user } = useAuth();
  const historyKey = user ? `${HISTORY_PREFIX}${user.id}:${id}` : "";
  const [query, setQuery] = useState("SELECT 1;");
  const [readOnly, setReadOnly] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [result, setResult] = useState<SQLResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const executeRef = useRef<() => void>(undefined);

  useEffect(() => deferEffect(() => {
    localStorage.removeItem("dupabase_sql_history");
    setHistory(historyKey ? loadHistory(historyKey) : []);
  }), [historyKey]);

  const execute = useCallback(async () => {
    if (!token || !query.trim()) return;
    setExecuting(true);
    setError(null);
    setResult(null);

    const { data, error: apiError } = await sqlApi.execute(
      token,
      id,
      query.trim(),
      readOnly
    );

    if (apiError) {
      setError(apiError);
      toast.error("Query failed");
    } else if (data) {
      setResult(data);
      toast.success(
        `Query executed in ${data.execution_time_ms}ms (${data.row_count} row${data.row_count !== 1 ? "s" : ""})`
      );
    }

    // Save to history
    const entry: HistoryEntry = { query: query.trim(), timestamp: Date.now() };
    const updated = [entry, ...history.filter((h) => h.query !== query.trim())].slice(
      0,
      MAX_HISTORY
    );
    setHistory(updated);
    saveHistory(historyKey, updated);

    setExecuting(false);
  }, [token, id, query, readOnly, history, historyKey]);

  // Keep executeRef in sync so the Monaco keybinding always calls the latest execute
  useEffect(() => {
    executeRef.current = execute;
  }, [execute]);

  const handleEditorMount: OnMount = (editor, monaco) => {
    // Cmd+Enter / Ctrl+Enter keybinding
    editor.addCommand(
      monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter,
      () => {
        executeRef.current?.();
      }
    );
  };

  const clearHistory = () => {
    setHistory([]);
    sessionStorage.removeItem(historyKey);
    toast.success("History cleared");
  };

  const loadFromHistory = (entry: HistoryEntry) => {
    setQuery(entry.query);
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center gap-3">
        <Button variant="ghost" size="icon" className="shrink-0" asChild>
          <Link href={`/dashboard/projects/${id}`}>
            <ArrowLeft className="h-4 w-4" />
          </Link>
        </Button>
        <h2 className="text-xl sm:text-2xl font-bold tracking-tight">
          SQL Editor
        </h2>
      </div>

      {/* Editor */}
      <Card>
        <CardHeader className="pb-3">
          <div className="flex items-center justify-between">
            <div>
              <CardTitle className="text-base">Query</CardTitle>
              <CardDescription className="text-xs">
                Press Cmd+Enter (or Ctrl+Enter) to execute
              </CardDescription>
            </div>
            <div className="flex items-center gap-4">
              {history.length > 0 && (
                <Select onValueChange={(val) => {
                  const entry = history.find((h) => String(h.timestamp) === val);
                  if (entry) loadFromHistory(entry);
                }}>
                  <SelectTrigger className="w-[200px] h-8 text-xs">
                    <History className="mr-1.5 h-3 w-3" />
                    <SelectValue placeholder="Recent queries" />
                  </SelectTrigger>
                  <SelectContent>
                    {history.map((h) => (
                      <SelectItem key={h.timestamp} value={String(h.timestamp)}>
                        <span className="font-mono text-xs truncate max-w-[160px] block">
                          {h.query.slice(0, 60)}
                          {h.query.length > 60 ? "..." : ""}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {history.length > 0 && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8"
                  onClick={clearHistory}
                  title="Clear history"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              )}
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="rounded-md border overflow-hidden">
            <Editor
              height="300px"
              language="sql"
              theme="vs-dark"
              value={query}
              onChange={(value) => setQuery(value ?? "")}
              onMount={handleEditorMount}
              loading={
                <div className="flex items-center justify-center h-[300px] bg-muted/50">
                  <Skeleton className="h-[260px] w-[calc(100%-2rem)]" />
                </div>
              }
              options={{
                minimap: { enabled: false },
                fontSize: 14,
                scrollBeyondLastLine: false,
                wordWrap: "on",
                lineNumbers: "on",
                renderLineHighlight: "line",
                automaticLayout: true,
                tabSize: 2,
                padding: { top: 8, bottom: 8 },
              }}
            />
          </div>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="flex items-center gap-2">
                <Switch
                  id="read-only"
                  checked={readOnly}
                  onCheckedChange={setReadOnly}
                />
                <Label htmlFor="read-only" className="text-xs">
                  Read Only
                </Label>
              </div>
            </div>
            <Button onClick={execute} disabled={executing || !query.trim()}>
              <Play className="mr-1.5 h-3.5 w-3.5" />
              {executing ? "Executing..." : "Execute"}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Status bar */}
      {(result || error) && (
        <div className="flex items-center gap-3 px-1">
          {error ? (
            <>
              <AlertCircle className="h-4 w-4 text-destructive shrink-0" />
              <p className="text-sm text-destructive">{error}</p>
            </>
          ) : result ? (
            <>
              <CheckCircle2 className="h-4 w-4 text-emerald-500 shrink-0" />
              <div className="flex items-center gap-3">
                <Badge variant="secondary" className="text-xs">
                  {result.row_count} row{result.row_count !== 1 ? "s" : ""}
                </Badge>
                <span className="flex items-center gap-1 text-xs text-muted-foreground">
                  <Clock className="h-3 w-3" />
                  {result.execution_time_ms}ms
                </span>
              </div>
            </>
          ) : null}
        </div>
      )}

      {/* Results table */}
      {result && result.columns && result.columns.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Results</CardTitle>
          </CardHeader>
          <CardContent className="p-0">
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    {result.columns.map((col) => (
                      <TableHead
                        key={col}
                        className="font-mono text-xs whitespace-nowrap"
                      >
                        {col}
                      </TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {result.rows.map((row, rowIdx) => (
                    <TableRow key={rowIdx}>
                      {row.map((cell, colIdx) => (
                        <TableCell
                          key={colIdx}
                          className="font-mono text-xs max-w-[300px] truncate"
                        >
                          {cell === null ? (
                            <span className="text-muted-foreground italic">
                              NULL
                            </span>
                          ) : typeof cell === "object" ? (
                            JSON.stringify(cell)
                          ) : (
                            String(cell)
                          )}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))}
                  {result.rows.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={result.columns.length}
                        className="text-center text-muted-foreground py-8"
                      >
                        Query returned no rows
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
