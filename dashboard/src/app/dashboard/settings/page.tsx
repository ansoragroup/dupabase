"use client";
import { useRouter } from "next/navigation";

import { useState } from "react";
import { useAuth } from "@/lib/auth-context";
import { platformAuth } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Loader2 } from "lucide-react";

export default function SettingsPage() {
  const router = useRouter();
  const { user, token, loading: authLoading, logout } = useAuth();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [loading, setLoading] = useState(false);

  const handleChangePassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    if (newPassword !== confirmPassword) {
      toast.error("New passwords do not match");
      return;
    }
    if (newPassword.length < 6) {
      toast.error("New password must be at least 6 characters");
      return;
    }
    setLoading(true);
    const { error } = await platformAuth.changePassword(
      token,
      currentPassword,
      newPassword
    );
    setLoading(false);
    if (error) {
      toast.error(error);
    } else {
      toast.success("Password changed successfully");
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
    }
  };

  if (authLoading) {
    return (
      <div className="space-y-6">
        <div>
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-64 mt-2" />
        </div>
        {[1, 2, 3].map((i) => (
          <div key={i}>
            {i > 1 && <Separator className="my-6" />}
            <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
              <div className="space-y-1.5">
                <Skeleton className="h-5 w-24" />
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
      <div>
        <h2 className="text-2xl font-bold tracking-tight">Account Settings</h2>
        <p className="text-muted-foreground">
          Manage your platform account
        </p>
      </div>

      {/* Profile Section */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">Profile</h3>
          <p className="text-sm text-muted-foreground">
            Your account information and identifiers.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6 space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">Email</Label>
                <div className="rounded-md border bg-muted/50 px-3 py-2">
                  <p className="text-sm font-mono">{user?.email}</p>
                </div>
              </div>
              <div className="space-y-1.5">
                <Label className="text-xs text-muted-foreground">PostgreSQL Username</Label>
                <div className="rounded-md border bg-muted/50 px-3 py-2">
                  <p className="text-sm font-mono">{user?.pg_username}</p>
                </div>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs text-muted-foreground">Member since</Label>
              <div className="rounded-md border bg-muted/50 px-3 py-2 w-fit">
                <p className="text-sm">
                  {user?.created_at
                    ? new Date(user.created_at).toLocaleDateString("en-US", {
                        year: "numeric",
                        month: "long",
                        day: "numeric",
                      })
                    : "\u2014"}
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Security Section */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">Security</h3>
          <p className="text-sm text-muted-foreground">
            Update your platform password. This will also re-encrypt your PostgreSQL credentials.
          </p>
        </div>
        <Card>
          <CardContent className="pt-6">
            <form onSubmit={handleChangePassword} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="current">Current password</Label>
                <Input
                  id="current"
                  type="password"
                  value={currentPassword}
                  onChange={(e) => setCurrentPassword(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="new">New password</Label>
                <Input
                  id="new"
                  type="password"
                  placeholder="Min 6 characters"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm">Confirm new password</Label>
                <Input
                  id="confirm"
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  required
                />
              </div>
              <div className="flex justify-end">
                <Button type="submit" disabled={loading}>
                  {loading ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Updating...
                    </>
                  ) : (
                    "Update password"
                  )}
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      </div>

      <Separator />

      {/* Danger Zone */}
      <div className="grid grid-cols-1 md:grid-cols-[280px_1fr] gap-6 md:gap-8">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium text-destructive">Danger Zone</h3>
          <p className="text-sm text-muted-foreground">
            Irreversible actions for your account.
          </p>
        </div>
        <Card className="border-destructive/30">
          <CardContent className="pt-6">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <p className="text-sm font-medium">Sign out</p>
                <p className="text-xs text-muted-foreground">
                  Sign out of your account on this device.
                </p>
              </div>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => {
                  logout();
                  router.push("/login");
                }}
              >
                Sign out
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
