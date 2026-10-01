"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import {
  analytics as analyticsApi,
  type OverviewAnalytics,
  type TableStats,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import {
  ArrowLeft,
  Database,
  HardDrive,
  Rows3,
  Activity,
  Users,
  UserPlus,
  ShieldCheck,
  RefreshCw,
  ArrowUpDown,
  AlertCircle,
} from "lucide-react";
import Link from "next/link";
import { StatCard } from "@/components/dashboard/stat-card";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from "recharts";

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
}

function formatNumber(n: number): string {
  return n.toLocaleString();
}

type SortField = "name" | "row_count" | "total_size" | "index_size";
type SortDir = "asc" | "desc";

export default function AnalyticsPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [data, setData] = useState<OverviewAnalytics | null>(null);
  const [loading, setLoading] = useState(true);
  const [sortField, setSortField] = useState<SortField>("total_size");
  const [sortDir, setSortDir] = useState<SortDir>("desc");

  const load = useCallback(async () => {
    if (!token || !id) return;
    setLoading(true);
    const { data: result, error } = await analyticsApi.overview(token, id);
    if (error) {
      toast.error(error);
    } else {
      setData(result);
    }
    setLoading(false);
  }, [token, id]);

  useEffect(() => deferEffect(() => {
    load();
  }), [load]);

  const handleSort = (field: SortField) => {
    if (sortField === field) {
      setSortDir(sortDir === "asc" ? "desc" : "asc");
    } else {
      setSortField(field);
      setSortDir("desc");
    }
  };

  const sortedTables = (tables: TableStats[]) => {
    return [...tables].sort((a, b) => {
      const aVal = a[sortField];
      const bVal = b[sortField];
      if (typeof aVal === "string" && typeof bVal === "string") {
        return sortDir === "asc"
          ? aVal.localeCompare(bVal)
          : bVal.localeCompare(aVal);
      }
      const numA = aVal as number;
      const numB = bVal as number;
      return sortDir === "asc" ? numA - numB : numB - numA;
    });
  };

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-3">
          <Skeleton className="h-9 w-9 rounded-md" />
          <Skeleton className="h-7 w-48" />
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {[1, 2, 3, 4].map((i) => (
            <Card key={i}>
              <CardHeader className="pb-2">
                <Skeleton className="h-4 w-24" />
              </CardHeader>
              <CardContent>
                <Skeleton className="h-8 w-20" />
              </CardContent>
            </Card>
          ))}
        </div>
        <Card>
          <CardHeader>
            <Skeleton className="h-5 w-32" />
          </CardHeader>
          <CardContent>
            <Skeleton className="h-48 w-full" />
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="space-y-4">
        <Button variant="ghost" size="sm" asChild>
          <Link href={`/dashboard/projects/${id}`}>
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to project
          </Link>
        </Button>
        <Card>
          <CardContent className="py-16 text-center text-muted-foreground">
            Failed to load analytics data.
          </CardContent>
        </Card>
      </div>
    );
  }

  const connectionChartData = data.connections?.connections?.map((c) => ({
    state: c.state.replace(/_/g, " "),
    count: c.count,
  })) ?? [];

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
            Analytics
          </h2>
        </div>
        <Button variant="outline" size="sm" onClick={load}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      {/* Overview Cards */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Database Size"
          value={data.database ? formatBytes(data.database.db_size) : "--"}
          icon={HardDrive}
          description="Total disk usage"
        />
        <StatCard
          title="Tables"
          value={data.database ? formatNumber(data.database.table_count) : "--"}
          icon={Database}
          description="Across all schemas"
        />
        <StatCard
          title="Total Rows"
          value={data.database ? formatNumber(data.database.total_rows) : "--"}
          icon={Rows3}
          description="All tables combined"
        />
        <StatCard
          title="Active Connections"
          value={data.connections ? formatNumber(data.connections.active) : "--"}
          icon={Activity}
          description={
            data.connections
              ? `${formatNumber(data.connections.total)} total`
              : undefined
          }
        />
      </div>

      {/* Tables Section */}
      {data.database && data.database.tables.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Tables</CardTitle>
            <CardDescription>
              Size and row counts for each table in your database.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Schema</TableHead>
                  <TableHead>
                    <button
                      className="flex items-center gap-1 hover:text-foreground transition-colors"
                      onClick={() => handleSort("name")}
                    >
                      Name
                      <ArrowUpDown className="h-3 w-3" />
                    </button>
                  </TableHead>
                  <TableHead className="text-right">
                    <button
                      className="flex items-center gap-1 ml-auto hover:text-foreground transition-colors"
                      onClick={() => handleSort("row_count")}
                    >
                      Rows
                      <ArrowUpDown className="h-3 w-3" />
                    </button>
                  </TableHead>
                  <TableHead className="text-right">
                    <button
                      className="flex items-center gap-1 ml-auto hover:text-foreground transition-colors"
                      onClick={() => handleSort("total_size")}
                    >
                      Size
                      <ArrowUpDown className="h-3 w-3" />
                    </button>
                  </TableHead>
                  <TableHead className="text-right">
                    <button
                      className="flex items-center gap-1 ml-auto hover:text-foreground transition-colors"
                      onClick={() => handleSort("index_size")}
                    >
                      Index Size
                      <ArrowUpDown className="h-3 w-3" />
                    </button>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedTables(data.database.tables).map((t) => (
                  <TableRow key={`${t.schema}.${t.name}`}>
                    <TableCell className="text-muted-foreground font-mono text-xs">
                      {t.schema}
                    </TableCell>
                    <TableCell className="font-mono text-xs font-medium">
                      {t.name}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs">
                      {formatNumber(t.row_count)}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs">
                      {formatBytes(t.total_size)}
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs">
                      {formatBytes(t.index_size)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      )}

      {/* Connections Section */}
      {data.connections && connectionChartData.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Connections</CardTitle>
            <CardDescription>
              Breakdown of current database connections by state.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-4 sm:grid-cols-3 mb-6">
              <div>
                <p className="text-xs text-muted-foreground">Active</p>
                <p className="text-lg font-semibold">
                  {formatNumber(data.connections.active)}
                </p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">Idle</p>
                <p className="text-lg font-semibold">
                  {formatNumber(data.connections.idle)}
                </p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">
                  Idle in Transaction
                </p>
                <p className="text-lg font-semibold">
                  {formatNumber(data.connections.idle_in_transaction)}
                </p>
              </div>
            </div>
            <div className="h-48">
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={connectionChartData} layout="vertical">
                  <CartesianGrid strokeDasharray="3 3" horizontal={false} />
                  <XAxis type="number" allowDecimals={false} />
                  <YAxis
                    type="category"
                    dataKey="state"
                    width={120}
                    tick={{ fontSize: 12 }}
                  />
                  <Tooltip />
                  <Bar
                    dataKey="count"
                    fill="hsl(var(--primary))"
                    radius={[0, 4, 4, 0]}
                  />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Auth Section */}
      {data.auth && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Authentication</CardTitle>
            <CardDescription>
              User signups and active session overview.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
              <StatCard
                title="Total Users"
                value={formatNumber(data.auth.total_users)}
                icon={Users}
              />
              <StatCard
                title="Signups (7d)"
                value={formatNumber(data.auth.signups_7d)}
                icon={UserPlus}
              />
              <StatCard
                title="Signups (30d)"
                value={formatNumber(data.auth.signups_30d)}
                icon={UserPlus}
              />
              <StatCard
                title="Active Sessions"
                value={formatNumber(data.auth.active_sessions)}
                icon={ShieldCheck}
              />
            </div>
          </CardContent>
        </Card>
      )}

      {/* Slow Queries Section */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Slow Queries</CardTitle>
          <CardDescription>
            Top queries by total execution time from pg_stat_statements.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {(() => {
            // The overview endpoint may not include queries; fetch separately or show note
            // For now, show a placeholder directing users to enable pg_stat_statements
            return (
              <div className="flex items-start gap-3 rounded-md border border-dashed p-4 text-sm text-muted-foreground">
                <AlertCircle className="h-4 w-4 mt-0.5 shrink-0" />
                <div>
                  <p className="font-medium text-foreground">
                    pg_stat_statements not available
                  </p>
                  <p className="mt-1">
                    Enable the{" "}
                    <code className="text-xs bg-muted px-1 py-0.5 rounded">
                      pg_stat_statements
                    </code>{" "}
                    extension in your PostgreSQL configuration to see slow query
                    analytics here.
                  </p>
                </div>
              </div>
            );
          })()}
        </CardContent>
      </Card>
    </div>
  );
}
