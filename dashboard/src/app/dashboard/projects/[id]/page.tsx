"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import { projects as projectsApi, type Project } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { toast } from "sonner";
import { ArrowLeft, BarChart3, Database, Download, Globe, Key, Settings, Upload, TableProperties, Terminal, Users, ScrollText } from "lucide-react";
import Link from "next/link";
import { SecretField } from "@/components/dashboard/secret-field";
import { CopyButton } from "@/components/dashboard/copy-button";
import { ImportDialog } from "@/components/dashboard/import-dialog";
import { ImportHistory } from "@/components/dashboard/import-history";
import { ExportSection } from "@/components/dashboard/export-section";

export default function ProjectDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [project, setProject] = useState<Project | null>(null);
  const [loading, setLoading] = useState(true);
  const [importRefreshKey, setImportRefreshKey] = useState(0);

  const load = useCallback(async () => {
    if (!token) return;
    const { data, error } = await projectsApi.get(token, id);
    if (error) {
      toast.error(error);
      setLoading(false);
      return;
    }
    setProject(data);
    setLoading(false);
  }, [token, id]);

  useEffect(() => deferEffect(() => {
    load();
  }), [load]);

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="flex items-start gap-3 sm:gap-4">
          <Skeleton className="h-9 w-9 rounded-md shrink-0" />
          <div className="flex-1">
            <div className="flex items-center gap-3">
              <Skeleton className="h-7 w-48" />
              <Skeleton className="h-5 w-14 rounded-full" />
            </div>
            <Skeleton className="h-4 w-32 mt-1.5" />
          </div>
          <Skeleton className="h-8 w-24 shrink-0" />
        </div>
        <div className="flex gap-4 border-b pb-px">
          {[1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-8 w-24" />
          ))}
        </div>
        <Card>
          <CardHeader>
            <Skeleton className="h-5 w-40" />
            <Skeleton className="h-4 w-64 mt-1" />
          </CardHeader>
          <CardContent className="space-y-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!project) {
    return (
      <div className="space-y-4">
        <Button variant="ghost" size="sm" asChild>
          <Link href="/dashboard">
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to projects
          </Link>
        </Button>
        <Card>
          <CardContent className="py-16 text-center text-muted-foreground">
            Project not found
          </CardContent>
        </Card>
      </div>
    );
  }

  const apiUrl = process.env.NEXT_PUBLIC_API_URL || "http://localhost:3333";

  const jsExample = `import { createClient } from '@supabase/supabase-js'

const SUPABASE_URL = '${apiUrl}'
const SUPABASE_ANON_KEY = '<your-anon-key>'

const supabase = createClient(SUPABASE_URL, SUPABASE_ANON_KEY)

// Auth
const { data } = await supabase.auth.signUp({
  email: 'user@example.com',
  password: 'password123',
})

// REST
const { data: todos } = await supabase
  .from('todos')
  .select('*')`;

  const connectionString = `postgresql://<username>:<password>@localhost:15432/${project.db_name}`;

  return (
    <div className="space-y-6">
      <div className="flex items-start gap-3 sm:gap-4">
        <Button variant="ghost" size="icon" className="shrink-0 mt-1" asChild>
          <Link href="/dashboard">
            <ArrowLeft className="h-4 w-4" />
          </Link>
        </Button>
        <div className="flex-1 min-w-0">
          <div className="flex flex-wrap items-center gap-2 sm:gap-3">
            <h2 className="text-xl sm:text-2xl font-bold tracking-tight truncate">
              {project.name}
            </h2>
            <Badge variant="secondary" className="gap-1.5">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
              Active
            </Badge>
          </div>
          <p className="text-sm text-muted-foreground font-mono truncate">
            {project.db_name}
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0 flex-wrap">
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/tables`}>
              <TableProperties className="mr-1.5 h-3.5 w-3.5" />
              Tables
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/sql`}>
              <Terminal className="mr-1.5 h-3.5 w-3.5" />
              SQL
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/auth`}>
              <Users className="mr-1.5 h-3.5 w-3.5" />
              Auth Users
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/logs`}>
              <ScrollText className="mr-1.5 h-3.5 w-3.5" />
              Logs
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/analytics`}>
              <BarChart3 className="mr-1.5 h-3.5 w-3.5" />
              Analytics
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={`/dashboard/projects/${id}/settings`}>
              <Settings className="mr-1.5 h-3.5 w-3.5" />
              Settings
            </Link>
          </Button>
        </div>
      </div>

      <Tabs defaultValue="api-keys">
        <TabsList className="w-max">
          <TabsTrigger value="api-keys">
            <Key className="mr-1.5 h-3.5 w-3.5" />
            API Keys
          </TabsTrigger>
          <TabsTrigger value="connection">
            <Database className="mr-1.5 h-3.5 w-3.5" />
            Connection
          </TabsTrigger>
          <TabsTrigger value="quickstart">
            <Globe className="mr-1.5 h-3.5 w-3.5" />
            Quick Start
          </TabsTrigger>
          <TabsTrigger value="import">
            <Upload className="mr-1.5 h-3.5 w-3.5" />
            Import
          </TabsTrigger>
          <TabsTrigger value="export">
            <Download className="mr-1.5 h-3.5 w-3.5" />
            Export
          </TabsTrigger>
        </TabsList>

        <TabsContent value="api-keys" className="space-y-4 mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Project API Keys</CardTitle>
              <CardDescription>
                Use these keys with @supabase/supabase-js to connect to your project.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-1.5">
                <p className="text-xs font-medium text-muted-foreground">
                  Project URL
                </p>
                <div className="flex items-center gap-2 rounded-md border bg-muted/50 px-3 py-2">
                  <code className="flex-1 text-xs font-mono">{apiUrl}</code>
                  <CopyButton value={apiUrl} className="h-7 w-7 shrink-0" />
                </div>
              </div>
              <Separator />
              <SecretField
                label="anon / public key"
                value={project.anon_key}
              />
              <SecretField
                label="service_role key (keep secret!)"
                value={project.service_role_key}
              />
              <Separator />
              <SecretField
                label="JWT Secret"
                value={project.jwt_secret}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">Project Settings</CardTitle>
              <CardDescription>
                Authentication configuration for this project.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
                <div>
                  <p className="text-xs text-muted-foreground">Signup</p>
                  <p className="text-sm font-medium">
                    {project.settings?.enable_signup ? "Enabled" : "Disabled"}
                  </p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">Auto-confirm</p>
                  <p className="text-sm font-medium">
                    {project.settings?.autoconfirm ? "Yes" : "No"}
                  </p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">
                    Min password length
                  </p>
                  <p className="text-sm font-medium">
                    {project.settings?.password_min_length ?? "\u2014"}
                  </p>
                </div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="connection" className="space-y-4 mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">
                Direct Database Connection
              </CardTitle>
              <CardDescription>
                Connect directly to the PostgreSQL database using your PG
                credentials. Reveal your password on the Credentials page.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-1.5">
                <p className="text-xs font-medium text-muted-foreground">
                  Connection string
                </p>
                <div className="flex items-center gap-2 rounded-md border bg-muted/50 px-3 py-2">
                  <code className="flex-1 text-xs font-mono break-all">
                    {connectionString}
                  </code>
                  <CopyButton
                    value={connectionString}
                    className="h-7 w-7 shrink-0"
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <p className="text-xs text-muted-foreground">Host</p>
                  <p className="text-sm font-mono">localhost</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">Port</p>
                  <p className="text-sm font-mono">15432</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">Database</p>
                  <p className="text-sm font-mono">{project.db_name}</p>
                </div>
                <div>
                  <p className="text-xs text-muted-foreground">Password</p>
                  <Button variant="link" size="sm" className="h-auto p-0" asChild>
                    <Link href="/dashboard/credentials">
                      Reveal on Credentials page
                    </Link>
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="quickstart" className="space-y-4 mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">
                Quick Start with @supabase/supabase-js
              </CardTitle>
              <CardDescription>
                Install the client library and connect to your project.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-1.5">
                <p className="text-xs font-medium text-muted-foreground">
                  1. Install
                </p>
                <div className="relative rounded-md border bg-muted/50 p-3">
                  <code className="text-xs font-mono">
                    npm install @supabase/supabase-js
                  </code>
                  <CopyButton
                    value="npm install @supabase/supabase-js"
                    className="absolute right-2 top-2 h-7 w-7"
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <p className="text-xs font-medium text-muted-foreground">
                  2. Connect
                </p>
                <div className="relative rounded-md border bg-muted/50 p-3 overflow-hidden">
                  <pre className="text-xs font-mono whitespace-pre-wrap break-all pr-8">
                    {jsExample}
                  </pre>
                  <CopyButton
                    value={jsExample}
                    className="absolute right-2 top-2 h-7 w-7"
                  />
                </div>
              </div>
              <div className="space-y-1.5">
                <p className="text-xs font-medium text-muted-foreground">
                  Your anon key (replace {'<your-anon-key>'} above)
                </p>
                <div className="flex items-center gap-2 rounded-md border bg-muted/50 px-3 py-2">
                  <code className="flex-1 text-xs font-mono break-all">
                    {project.anon_key}
                  </code>
                  <CopyButton
                    value={project.anon_key || ""}
                    className="h-7 w-7 shrink-0"
                  />
                </div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="import" className="space-y-4 mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Database Import</CardTitle>
              <CardDescription>
                Import data from a Supabase export or any PostgreSQL dump file.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ImportDialog
                projectId={id}
                onComplete={() => setImportRefreshKey((k) => k + 1)}
              />
            </CardContent>
          </Card>
          <ImportHistory projectId={id} refreshKey={importRefreshKey} />
        </TabsContent>

        <TabsContent value="export" className="space-y-4 mt-4">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Database Export</CardTitle>
              <CardDescription>
                Download a full export of your project database as a .dump or .sql file.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ExportSection projectId={id} />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
