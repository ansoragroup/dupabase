"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import {
  authUsers as authUsersApi,
  type AuthUserInfo,
  type AuthUserDetail,
  type AuthSessionInfo,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Separator } from "@/components/ui/separator";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { toast } from "sonner";
import {
  ArrowLeft,
  Search,
  Users,
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  ShieldBan,
  ShieldCheck,
  Trash2,
  Ghost,
  Monitor,
} from "lucide-react";
import Link from "next/link";

function formatDate(dateStr: string | null): string {
  if (!dateStr) return "\u2014";
  return new Date(dateStr).toLocaleString();
}

function getUserStatus(user: AuthUserInfo): {
  label: string;
  variant: "default" | "secondary" | "destructive" | "outline";
} {
  if (user.banned_until) {
    const bannedDate = new Date(user.banned_until);
    if (bannedDate > new Date()) {
      return { label: "Banned", variant: "destructive" };
    }
  }
  if (user.is_anonymous) {
    return { label: "Anonymous", variant: "outline" };
  }
  if (user.email_confirmed_at || user.phone_confirmed_at) {
    return { label: "Confirmed", variant: "default" };
  }
  return { label: "Unconfirmed", variant: "secondary" };
}

export default function AuthUsersPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [users, setUsers] = useState<AuthUserInfo[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [perPage] = useState(50);
  const [search, setSearch] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [loading, setLoading] = useState(true);
  const [selectedUser, setSelectedUser] = useState<AuthUserDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);

  const loadUsers = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    const { data, error } = await authUsersApi.list(token, id, {
      page,
      perPage,
      search,
    });
    if (error) {
      toast.error(error);
    } else if (data) {
      setUsers(data.users);
      setTotal(data.total);
    }
    setLoading(false);
  }, [token, id, page, perPage, search]);

  useEffect(() => deferEffect(() => {
    loadUsers();
  }), [loadUsers]);

  const handleSearch = () => {
    setSearch(searchInput);
    setPage(1);
  };

  const openUserDetail = async (userId: string) => {
    if (!token) return;
    setSheetOpen(true);
    setDetailLoading(true);
    setSelectedUser(null);
    const { data, error } = await authUsersApi.get(token, id, userId);
    if (error) {
      toast.error(error);
      setSheetOpen(false);
    } else if (data) {
      setSelectedUser(data);
    }
    setDetailLoading(false);
  };

  const handleBan = async (userId: string) => {
    if (!token) return;
    const { error } = await authUsersApi.ban(token, id, userId);
    if (error) {
      toast.error(error);
    } else {
      toast.success("User banned");
      loadUsers();
      if (selectedUser?.id === userId) {
        openUserDetail(userId);
      }
    }
  };

  const handleUnban = async (userId: string) => {
    if (!token) return;
    const { error } = await authUsersApi.unban(token, id, userId);
    if (error) {
      toast.error(error);
    } else {
      toast.success("User unbanned");
      loadUsers();
      if (selectedUser?.id === userId) {
        openUserDetail(userId);
      }
    }
  };

  const handleDelete = async (userId: string) => {
    if (!token) return;
    const { error } = await authUsersApi.delete(token, id, userId);
    if (error) {
      toast.error(error);
    } else {
      toast.success("User deleted");
      setSheetOpen(false);
      setSelectedUser(null);
      loadUsers();
    }
  };

  const totalPages = Math.ceil(total / perPage);

  if (loading && users.length === 0) {
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
          <div>
            <h2 className="text-xl sm:text-2xl font-bold tracking-tight">
              Auth Users
            </h2>
            <p className="text-sm text-muted-foreground">
              {total} user{total !== 1 ? "s" : ""} total
            </p>
          </div>
        </div>
        <Button variant="outline" size="sm" onClick={loadUsers}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      {/* Search */}
      <div className="flex items-center gap-2">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Search by email or phone..."
            className="pl-9"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") handleSearch();
            }}
          />
        </div>
        <Button variant="outline" onClick={handleSearch}>
          Search
        </Button>
      </div>

      {/* Users table */}
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Email / Phone</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last Sign In</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((user) => {
                const status = getUserStatus(user);
                return (
                  <TableRow
                    key={user.id}
                    className="cursor-pointer"
                    onClick={() => openUserDetail(user.id)}
                  >
                    <TableCell>
                      <div>
                        <p className="text-sm font-medium">
                          {user.email || user.phone || (
                            <span className="flex items-center gap-1 text-muted-foreground">
                              <Ghost className="h-3.5 w-3.5" />
                              Anonymous
                            </span>
                          )}
                        </p>
                        <p className="text-xs text-muted-foreground font-mono">
                          {user.id.slice(0, 8)}...
                        </p>
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDate(user.created_at)}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatDate(user.last_sign_in_at)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={status.variant} className="text-[10px]">
                        {status.label}
                      </Badge>
                    </TableCell>
                  </TableRow>
                );
              })}
              {users.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={4}
                    className="text-center text-muted-foreground py-8"
                  >
                    <Users className="h-8 w-8 mx-auto mb-2 opacity-50" />
                    No users found
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>

          {/* Pagination */}
          {totalPages > 1 && (
            <div className="flex items-center justify-between px-4 py-3 border-t">
              <p className="text-xs text-muted-foreground">
                Page {page} of {totalPages}
              </p>
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
                  disabled={page >= totalPages}
                  onClick={() => setPage(page + 1)}
                >
                  <ChevronRight className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      {/* User detail sheet */}
      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent className="overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>User Details</SheetTitle>
            <SheetDescription>
              {selectedUser?.email || selectedUser?.phone || "Anonymous user"}
            </SheetDescription>
          </SheetHeader>
          {detailLoading ? (
            <div className="space-y-4 p-4">
              {[1, 2, 3, 4].map((i) => (
                <Skeleton key={i} className="h-6 w-full" />
              ))}
            </div>
          ) : selectedUser ? (
            <div className="space-y-6 p-4">
              {/* User info */}
              <div className="space-y-3">
                <h4 className="text-sm font-semibold">Information</h4>
                <div className="grid grid-cols-2 gap-3 text-sm">
                  <div>
                    <p className="text-xs text-muted-foreground">ID</p>
                    <p className="font-mono text-xs break-all">{selectedUser.id}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Email</p>
                    <p className="text-xs">{selectedUser.email || "\u2014"}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Phone</p>
                    <p className="text-xs">{selectedUser.phone || "\u2014"}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Anonymous</p>
                    <p className="text-xs">{selectedUser.is_anonymous ? "Yes" : "No"}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Email Confirmed</p>
                    <p className="text-xs">{formatDate(selectedUser.email_confirmed_at)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Phone Confirmed</p>
                    <p className="text-xs">{formatDate(selectedUser.phone_confirmed_at)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Last Sign In</p>
                    <p className="text-xs">{formatDate(selectedUser.last_sign_in_at)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Banned Until</p>
                    <p className="text-xs">{formatDate(selectedUser.banned_until)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Created</p>
                    <p className="text-xs">{formatDate(selectedUser.created_at)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Updated</p>
                    <p className="text-xs">{formatDate(selectedUser.updated_at)}</p>
                  </div>
                </div>
              </div>

              <Separator />

              {/* Sessions */}
              <div className="space-y-3">
                <h4 className="text-sm font-semibold flex items-center gap-2">
                  <Monitor className="h-4 w-4" />
                  Sessions ({selectedUser.sessions?.length || 0})
                </h4>
                {selectedUser.sessions && selectedUser.sessions.length > 0 ? (
                  <div className="space-y-2">
                    {selectedUser.sessions.map((session: AuthSessionInfo) => (
                      <div
                        key={session.id}
                        className="rounded-md border p-3 text-xs space-y-1"
                      >
                        <div className="flex items-center justify-between">
                          <span className="font-mono text-muted-foreground">
                            {session.id.slice(0, 8)}...
                          </span>
                          <span className="text-muted-foreground">
                            {formatDate(session.created_at)}
                          </span>
                        </div>
                        {session.ip && (
                          <p className="text-muted-foreground">IP: {session.ip}</p>
                        )}
                        {session.user_agent && (
                          <p className="text-muted-foreground truncate">
                            UA: {session.user_agent}
                          </p>
                        )}
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground">No active sessions</p>
                )}
              </div>

              <Separator />

              {/* Metadata */}
              <div className="space-y-3">
                <h4 className="text-sm font-semibold">App Metadata</h4>
                <pre className="rounded-md border bg-muted/50 p-3 text-xs font-mono overflow-auto max-h-[200px]">
                  {JSON.stringify(selectedUser.app_metadata, null, 2) || "{}"}
                </pre>
              </div>

              <div className="space-y-3">
                <h4 className="text-sm font-semibold">User Metadata</h4>
                <pre className="rounded-md border bg-muted/50 p-3 text-xs font-mono overflow-auto max-h-[200px]">
                  {JSON.stringify(selectedUser.user_metadata, null, 2) || "{}"}
                </pre>
              </div>

              <Separator />

              {/* Actions */}
              <div className="flex items-center gap-2">
                {selectedUser.banned_until &&
                new Date(selectedUser.banned_until) > new Date() ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => handleUnban(selectedUser.id)}
                  >
                    <ShieldCheck className="mr-1.5 h-3.5 w-3.5" />
                    Unban
                  </Button>
                ) : (
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button variant="outline" size="sm">
                        <ShieldBan className="mr-1.5 h-3.5 w-3.5" />
                        Ban
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>Ban User</AlertDialogTitle>
                        <AlertDialogDescription>
                          Are you sure you want to ban this user? They will not
                          be able to sign in.
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>Cancel</AlertDialogCancel>
                        <AlertDialogAction
                          onClick={() => handleBan(selectedUser.id)}
                        >
                          Ban User
                        </AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                )}

                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button variant="destructive" size="sm">
                      <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                      Delete
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Delete User</AlertDialogTitle>
                      <AlertDialogDescription>
                        Are you sure you want to permanently delete this user?
                        This action cannot be undone.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction
                        variant="destructive"
                        onClick={() => handleDelete(selectedUser.id)}
                      >
                        Delete
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </div>
            </div>
          ) : null}
        </SheetContent>
      </Sheet>
    </div>
  );
}
