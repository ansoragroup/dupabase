"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useState, useEffect, useCallback } from "react";
import { useParams, useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-context";
import { useOrg } from "@/lib/org-context";
import { orgs as orgsApi, type OrgDetail } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { toast } from "sonner";
import { Loader2, Trash2 } from "lucide-react";

export default function OrgSettingsPage() {
  const { id } = useParams<{ id: string }>();
  const { token, user } = useAuth();
  const { refreshOrgs } = useOrg();
  const router = useRouter();
  const [org, setOrg] = useState<OrgDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [saving, setSaving] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleteConfirm, setDeleteConfirm] = useState("");
  const [deleting, setDeleting] = useState(false);

  const loadOrg = useCallback(async () => {
    if (!token || !id) return;
    const { data, error } = await orgsApi.get(token, id);
    if (error) {
      toast.error(error);
    } else if (data) {
      setOrg(data);
      setName(data.name);
      setSlug(data.slug);
    }
    setLoading(false);
  }, [token, id]);

  useEffect(() => deferEffect(() => {
    loadOrg();
  }), [loadOrg]);

  const handleSave = async () => {
    if (!token || !id || !name.trim() || !slug.trim()) return;
    setSaving(true);
    const { error } = await orgsApi.update(token, id, {
      name: name.trim(),
      slug: slug.trim(),
    });
    setSaving(false);
    if (error) {
      toast.error(error);
    } else {
      toast.success("Organization updated");
      await refreshOrgs();
      loadOrg();
    }
  };

  const handleDelete = async () => {
    if (!token || !id) return;
    setDeleting(true);
    const { error } = await orgsApi.delete(token, id);
    setDeleting(false);
    if (error) {
      toast.error(error);
    } else {
      toast.success("Organization deleted");
      await refreshOrgs();
      router.push("/dashboard");
    }
  };

  if (loading) {
    return (
      <div className="space-y-6 max-w-2xl">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-64" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const isOwner = org?.created_by === user?.id;

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <h2 className="text-2xl font-bold tracking-tight">Organization Settings</h2>
        <p className="text-muted-foreground">
          Manage your organization details.
        </p>
      </div>

      {/* General settings */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">General</CardTitle>
          <CardDescription>
            Update your organization name and slug.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="org-name">Name</Label>
            <Input
              id="org-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="org-slug">Slug</Label>
            <Input
              id="org-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              URL-friendly identifier for your organization.
            </p>
          </div>
          <Button
            onClick={handleSave}
            disabled={saving || !name.trim() || !slug.trim()}
          >
            {saving ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Saving...
              </>
            ) : (
              "Save changes"
            )}
          </Button>
        </CardContent>
      </Card>

      {/* Danger zone */}
      {isOwner && (
        <Card className="border-destructive/50">
          <CardHeader>
            <CardTitle className="text-base text-destructive">Danger Zone</CardTitle>
            <CardDescription>
              Irreversible actions that affect your entire organization.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex items-center justify-between">
              <div>
                <p className="text-sm font-medium">Delete organization</p>
                <p className="text-sm text-muted-foreground">
                  Permanently delete this organization and remove all members.
                </p>
              </div>
              <Button
                variant="destructive"
                onClick={() => setDeleteOpen(true)}
              >
                <Trash2 className="mr-2 h-4 w-4" />
                Delete
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Delete confirmation */}
      <AlertDialog open={deleteOpen} onOpenChange={(open) => {
        if (!open) {
          setDeleteOpen(false);
          setDeleteConfirm("");
        }
      }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete organization?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete <strong>{org?.name}</strong> and remove all members.
              This action cannot be undone. Type the organization name to confirm.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="py-4">
            <Input
              placeholder={org?.name}
              value={deleteConfirm}
              onChange={(e) => setDeleteConfirm(e.target.value)}
            />
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={deleteConfirm !== org?.name || deleting}
              onClick={handleDelete}
            >
              {deleting ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Deleting...
                </>
              ) : (
                "Delete organization"
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
