"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import {
  projects as projectsApi,
  type Project,
  type ProjectSettings,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Switch } from "@/components/ui/switch";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { ArrowLeft, Loader2, Save } from "lucide-react";
import Link from "next/link";

export default function ProjectSettingsPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [project, setProject] = useState<Project | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [settings, setSettings] = useState<ProjectSettings>({
    enable_signup: true,
    autoconfirm: true,
    password_min_length: 6,
  });

  const load = useCallback(async () => {
    if (!token) return;
    const { data, error } = await projectsApi.get(token, id);
    if (error) {
      toast.error(error);
      setLoading(false);
      return;
    }
    const found = data;
    if (found) {
      setProject(found);
      if (found.settings) {
        setSettings(found.settings);
      }
    }
    setLoading(false);
  }, [token, id]);

  useEffect(() => deferEffect(() => {
    load();
  }), [load]);

  const handleSave = async () => {
    if (!token || !project) return;
    setSaving(true);
    const { data, error } = await projectsApi.updateSettings(
      token,
      project.id,
      settings
    );
    setSaving(false);
    if (error) {
      toast.error(error);
    } else if (data) {
      setSettings(data);
      toast.success("Settings updated");
    }
  };

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-4">
          <Skeleton className="h-9 w-9 rounded-md" />
          <div>
            <Skeleton className="h-7 w-48" />
            <Skeleton className="h-4 w-32 mt-1.5" />
          </div>
        </div>
        {[1, 2, 3].map((i) => (
          <div key={i}>
            {i > 1 && <Separator className="my-6" />}
            <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
              <div className="space-y-1.5">
                <Skeleton className="h-5 w-28" />
                <Skeleton className="h-4 w-48" />
              </div>
              <Card>
                <CardContent className="pt-6">
                  <Skeleton className="h-10 w-full" />
                </CardContent>
              </Card>
            </div>
          </div>
        ))}
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

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <Button variant="ghost" size="icon" asChild>
          <Link href={`/dashboard/projects/${id}`}>
            <ArrowLeft className="h-4 w-4" />
          </Link>
        </Button>
        <div>
          <h2 className="text-2xl font-bold tracking-tight">
            {project.name} — Settings
          </h2>
          <p className="text-sm text-muted-foreground font-mono">
            {project.db_name}
          </p>
        </div>
      </div>

      {/* User Signups */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">User Signups</h3>
          <p className="text-sm text-muted-foreground">
            Control whether new users can create accounts via the auth API.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label>Enable Signup</Label>
                <p className="text-xs text-muted-foreground">
                  Allow new users to register through the auth endpoints
                </p>
              </div>
              <Switch
                checked={settings.enable_signup}
                onCheckedChange={(checked) =>
                  setSettings((s) => ({ ...s, enable_signup: checked }))
                }
              />
            </div>
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Email Confirmation */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">Email Confirmation</h3>
          <p className="text-sm text-muted-foreground">
            Configure whether new users are automatically confirmed or require email verification.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label>Auto-confirm</Label>
                <p className="text-xs text-muted-foreground">
                  Automatically confirm new users without email verification
                </p>
              </div>
              <Switch
                checked={settings.autoconfirm}
                onCheckedChange={(checked) =>
                  setSettings((s) => ({ ...s, autoconfirm: checked }))
                }
              />
            </div>
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Password Policy */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">Password Policy</h3>
          <p className="text-sm text-muted-foreground">
            Set the minimum password requirements for users signing up through auth.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6 space-y-3">
            <div className="space-y-2">
              <Label>Minimum Password Length</Label>
              <Input
                type="number"
                min={6}
                max={128}
                className="w-24"
                value={settings.password_min_length}
                onChange={(e) =>
                  setSettings((s) => ({
                    ...s,
                    password_min_length: parseInt(e.target.value) || 6,
                  }))
                }
              />
              <p className="text-xs text-muted-foreground">
                Minimum number of characters required (min 6, max 128)
              </p>
            </div>
          </CardContent>
        </Card>
      </div>

      <div className="flex justify-end pt-2">
        <Button onClick={handleSave} disabled={saving}>
          {saving ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              Saving...
            </>
          ) : (
            <>
              <Save className="mr-2 h-4 w-4" />
              Save settings
            </>
          )}
        </Button>
      </div>
    </div>
  );
}
