"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useState, useEffect, useCallback } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-context";
import {
  admin,
  type AdminUser,
  type AdminPlatformSettings,
  type AdminInvite,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
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
import {
  Users,
  ShieldCheck,
  Ticket,
  Copy,
  Trash2,
  Plus,
  FolderKanban,
  ChevronLeft,
  ChevronRight,
} from "lucide-react";
import { toast } from "sonner";

const USERS_PER_PAGE = 10;

export default function AdminPage() {
  const { user, token } = useAuth();
  const router = useRouter();

  const [users, setUsers] = useState<AdminUser[]>([]);
  const [totalUsers, setTotalUsers] = useState(0);
  const [usersPage, setUsersPage] = useState(1);
  const [settings, setSettings] = useState<AdminPlatformSettings | null>(null);
  const [invites, setInvites] = useState<AdminInvite[]>([]);
  const [loading, setLoading] = useState(true);
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null);
  const [inviteEmail, setInviteEmail] = useState("");
  const [creatingInvite, setCreatingInvite] = useState(false);

  const loadUsers = useCallback(async (page: number) => {
    if (!token) return;
    const res = await admin.getUsers(token, page, USERS_PER_PAGE);
    if (res.data) {
      setUsers(res.data.users);
      setTotalUsers(res.data.total);
      setUsersPage(res.data.page);
    }
  }, [token]);

  const loadData = useCallback(async () => {
    if (!token) return;
    const [usersRes, settingsRes, invitesRes] = await Promise.all([
      admin.getUsers(token, 1, USERS_PER_PAGE),
      admin.getSettings(token),
      admin.getInvites(token),
    ]);
    if (usersRes.data) {
      setUsers(usersRes.data.users);
      setTotalUsers(usersRes.data.total);
      setUsersPage(usersRes.data.page);
    }
    if (settingsRes.data) setSettings(settingsRes.data);
    if (invitesRes.data) setInvites(invitesRes.data);
    setLoading(false);
  }, [token]);

  useEffect(() => deferEffect(() => {
    if (!user?.is_admin) {
      router.replace("/dashboard");
      return;
    }
    loadData();
  }), [user, router, loadData]);

  const handleModeChange = async (mode: string) => {
    if (!token) return;
    const { error } = await admin.updateSettings(token, {
      registration_mode: mode as AdminPlatformSettings["registration_mode"],
    });
    if (error) {
      toast.error(error);
    } else {
      setSettings({ registration_mode: mode as AdminPlatformSettings["registration_mode"] });
      toast.success(`Registration mode set to "${mode}"`);
    }
  };

  const handleDeleteUser = async () => {
    if (!token || !deleteTarget) return;
    const { error } = await admin.deleteUser(token, deleteTarget.id);
    if (error) {
      toast.error(error);
    } else {
      toast.success(`User ${deleteTarget.email} deleted`);
      loadUsers(usersPage);
    }
    setDeleteTarget(null);
  };

  const handleCreateInvite = async () => {
    if (!token) return;
    setCreatingInvite(true);
    const { data, error } = await admin.createInvite(token, inviteEmail || undefined);
    setCreatingInvite(false);
    if (error) {
      toast.error(error);
    } else if (data) {
      setInvites((prev) => [data, ...prev]);
      setInviteEmail("");
      navigator.clipboard.writeText(data.code);
      toast.success("Invite created and code copied to clipboard");
    }
  };

  const handleDeleteInvite = async (id: string) => {
    if (!token) return;
    const { error } = await admin.deleteInvite(token, id);
    if (error) {
      toast.error(error);
    } else {
      setInvites((prev) => prev.filter((i) => i.id !== id));
      toast.success("Invite revoked");
    }
  };

  const copyCode = (code: string) => {
    navigator.clipboard.writeText(code);
    toast.success("Invite code copied");
  };

  if (loading) {
    return (
      <div className="space-y-8 max-w-4xl">
        <div>
          <Skeleton className="h-8 w-48 mb-2" />
          <Skeleton className="h-4 w-96" />
        </div>
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const activeInvites = invites.filter((i) => !i.used_by && new Date(i.expires_at) > new Date());
  const totalPages = Math.ceil(totalUsers / USERS_PER_PAGE);

  return (
    <div className="space-y-8 max-w-4xl">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Administration</h1>
        <p className="text-muted-foreground">
          Manage platform users, registration, and invites.
        </p>
      </div>

      {/* Registration Mode */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div>
          <h3 className="font-semibold">Registration</h3>
          <p className="text-sm text-muted-foreground mt-1">
            Control how new users can sign up for the platform.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6 space-y-4">
            <div className="space-y-2">
              <Label>Registration mode</Label>
              <Select
                value={settings?.registration_mode ?? "open"}
                onValueChange={handleModeChange}
              >
                <SelectTrigger className="w-full sm:w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="open">Open — anyone can register</SelectItem>
                  <SelectItem value="invite">Invite only — requires invite code</SelectItem>
                  <SelectItem value="disabled">Disabled — no new registrations</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center gap-2">
              <Badge
                variant={
                  settings?.registration_mode === "open"
                    ? "default"
                    : settings?.registration_mode === "invite"
                    ? "secondary"
                    : "destructive"
                }
              >
                {settings?.registration_mode === "open" && "Open"}
                {settings?.registration_mode === "invite" && "Invite Only"}
                {settings?.registration_mode === "disabled" && "Disabled"}
              </Badge>
              <span className="text-sm text-muted-foreground">
                {totalUsers} registered user{totalUsers !== 1 && "s"}
              </span>
            </div>
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Invite Management */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div>
          <h3 className="font-semibold">Invites</h3>
          <p className="text-sm text-muted-foreground mt-1">
            Generate invite codes for new users. Codes expire after 72 hours by default.
          </p>
        </div>
        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Ticket className="h-4 w-4" />
              Invite Codes
              {activeInvites.length > 0 && (
                <Badge variant="secondary">{activeInvites.length} active</Badge>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex gap-2">
              <Input
                placeholder="Email (optional)"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                className="max-w-xs"
              />
              <Button onClick={handleCreateInvite} disabled={creatingInvite}>
                <Plus className="h-4 w-4 mr-1" />
                {creatingInvite ? "Creating..." : "Generate"}
              </Button>
            </div>
            {invites.length > 0 && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Code</TableHead>
                    <TableHead>Email</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Expires</TableHead>
                    <TableHead className="w-[80px]" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {invites.map((inv) => {
                    const expired = new Date(inv.expires_at) < new Date();
                    const used = !!inv.used_by;
                    return (
                      <TableRow key={inv.id}>
                        <TableCell>
                          <button
                            onClick={() => copyCode(inv.code)}
                            className="font-mono text-xs hover:text-primary flex items-center gap-1"
                          >
                            {inv.code.slice(0, 8)}...
                            <Copy className="h-3 w-3" />
                          </button>
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {inv.email ?? "—"}
                        </TableCell>
                        <TableCell>
                          {used ? (
                            <Badge variant="secondary">Used</Badge>
                          ) : expired ? (
                            <Badge variant="destructive">Expired</Badge>
                          ) : (
                            <Badge>Active</Badge>
                          )}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {new Date(inv.expires_at).toLocaleDateString()}
                        </TableCell>
                        <TableCell>
                          {!used && (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7"
                              onClick={() => handleDeleteInvite(inv.id)}
                            >
                              <Trash2 className="h-3.5 w-3.5" />
                            </Button>
                          )}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            )}
            {invites.length === 0 && (
              <p className="text-sm text-muted-foreground">No invites yet.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Users */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div>
          <h3 className="font-semibold">Users</h3>
          <p className="text-sm text-muted-foreground mt-1">
            All registered platform users and their projects.
          </p>
        </div>
        <Card>
          <CardHeader>
            <CardTitle className="text-base flex items-center gap-2">
              <Users className="h-4 w-4" />
              Platform Users
              <Badge variant="secondary">{totalUsers}</Badge>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Email</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Projects</TableHead>
                  <TableHead>Joined</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell>
                      <div>
                        <span className="font-medium">{u.email}</span>
                        <p className="text-xs text-muted-foreground font-mono">
                          {u.pg_username}
                        </p>
                      </div>
                    </TableCell>
                    <TableCell>
                      {u.is_admin ? (
                        <Badge className="bg-emerald-500/10 text-emerald-400 border-emerald-500/20">
                          <ShieldCheck className="h-3 w-3 mr-1" />
                          Admin
                        </Badge>
                      ) : (
                        <Badge variant="secondary">User</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <span className="flex items-center gap-1 text-sm">
                        <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
                        {u.project_count}
                      </span>
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {new Date(u.created_at).toLocaleDateString()}
                    </TableCell>
                    <TableCell>
                      {!u.is_admin && u.id !== user?.id && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7 text-destructive hover:text-destructive"
                          onClick={() => setDeleteTarget(u)}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>

            {/* Pagination */}
            {totalPages > 1 && (
              <div className="flex items-center justify-between pt-2 border-t">
                <p className="text-sm text-muted-foreground">
                  Showing {(usersPage - 1) * USERS_PER_PAGE + 1}–{Math.min(usersPage * USERS_PER_PAGE, totalUsers)} of {totalUsers}
                </p>
                <div className="flex items-center gap-1">
                  <Button
                    variant="outline"
                    size="icon"
                    className="h-8 w-8"
                    disabled={usersPage <= 1}
                    onClick={() => loadUsers(usersPage - 1)}
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </Button>
                  <span className="text-sm px-2 min-w-[80px] text-center">
                    Page {usersPage} of {totalPages}
                  </span>
                  <Button
                    variant="outline"
                    size="icon"
                    className="h-8 w-8"
                    disabled={usersPage >= totalPages}
                    onClick={() => loadUsers(usersPage + 1)}
                  >
                    <ChevronRight className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Delete user dialog */}
      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete user?</AlertDialogTitle>
            <AlertDialogDescription>
              This will permanently delete <strong>{deleteTarget?.email}</strong> and
              all their projects. This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDeleteUser}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
