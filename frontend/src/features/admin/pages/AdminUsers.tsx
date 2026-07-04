import { useCallback, useEffect, useState } from "react";
import { UserPlus } from "lucide-react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { Input } from "@/shared/ui/Input";
import { Button } from "@/shared/ui/Button";
import { Badge } from "@/shared/ui/Badge";
import { Avatar, AvatarFallback } from "@/shared/ui/Avatar";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/ui/Table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/Dialog";
import { Label } from "@/shared/ui/Label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/Select";
import { ErrorState, ListSkeleton, PageHeader, SearchField } from "@/shared/ui";
import { adminListUsers, type ApiAccount } from "@/features/admin/api";
import { register } from "@/features/auth";
import { ApiError } from "@/shared/api/client";
import { formatDate } from "@/shared/lib/format";
import type { Role } from "@/shared/types";

// Подписи ролей — в словаре, здесь только ключи и вариант бейджа.
const ROLE_META = {
  user: { labelKey: "users.roles.user", variant: "outline" },
  operator: { labelKey: "users.roles.operator", variant: "secondary" },
  admin: { labelKey: "users.roles.admin", variant: "destructive" },
} as const satisfies Record<Role, { labelKey: string; variant: string }>;

function isKnownRole(r: string): r is Role {
  return r === "user" || r === "operator" || r === "admin";
}

/** Initials from a login: «anna.petrova» → AP, «admin» → AD. */
function initials(username: string): string {
  const parts = username.split(/[\s._-]+/).filter(Boolean);
  const [first, second] = parts;
  const two = first && second ? `${first.charAt(0)}${second.charAt(0)}` : username.slice(0, 2);
  return two.toUpperCase();
}

const MIN_PASSWORD = 6;

type Status = "loading" | "done" | "error";

export function AdminUsers() {
  const { t } = useTranslation("admin");
  const [accounts, setAccounts] = useState<ApiAccount[]>([]);
  const [status, setStatus] = useState<Status>("loading");
  const [filter, setFilter] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newRole, setNewRole] = useState<Role>("user");
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setStatus("loading");
    try {
      const list = await adminListUsers();
      setAccounts(
        [...list].toSorted(
          (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
        ),
      );
      setStatus("done");
    } catch {
      setStatus("error");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const filtered = accounts.filter((u) =>
    u.username.toLowerCase().includes(filter.trim().toLowerCase()),
  );

  const resetForm = () => {
    setNewUsername("");
    setNewPassword("");
    setNewRole("user");
  };

  const closeDialog = () => {
    setCreateOpen(false);
    resetForm();
  };

  const canSubmit = newUsername.trim().length > 0 && newPassword.length >= MIN_PASSWORD;

  const createUser = async () => {
    if (!canSubmit || creating) return;
    const username = newUsername.trim();
    setCreating(true);
    try {
      // register returns the tokens of the NEW user — we intentionally ignore them
      // (don't persist via saveAuth), so as not to drop the current admin session.
      await register(username, newPassword, [newRole]);
      toast.success(t("users.toasts.created", { username }));
      closeDialog();
      void load();
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : t("users.toasts.network");
      // Сообщение сервера может прийти и по-русски — регексп проверяет оба языка.
      if (/duplicate|already exists|существует/i.test(msg)) {
        toast.error(t("users.toasts.exists", { username }));
      } else {
        toast.error(t("users.toasts.createFailed", { message: msg }));
      }
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="mx-auto w-full max-w-5xl px-4 py-6 md:py-8">
      <PageHeader
        title={t("users.title")}
        description={t("users.description")}
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <UserPlus className="size-4" aria-hidden />
            {t("users.create")}
          </Button>
        }
      />

      <SearchField
        className="mt-5 sm:max-w-sm"
        value={filter}
        onChange={setFilter}
        placeholder={t("users.searchPlaceholder")}
        ariaLabel={t("users.searchAria")}
      />

      {status === "loading" && <ListSkeleton variant="avatar" ariaLabel={t("users.loadingAria")} />}

      {status === "error" && (
        <ErrorState message={t("users.loadError")} onRetry={() => void load()} />
      )}

      {status === "done" && (
        <div className="mt-4 rounded-lg border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("users.table.user")}</TableHead>
                <TableHead>{t("users.table.roles")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("users.table.created")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="max-w-56">
                    <div className="flex items-center gap-2.5">
                      <Avatar size="sm">
                        <AvatarFallback>{initials(u.username)}</AvatarFallback>
                      </Avatar>
                      <span className="truncate font-medium">{u.username}</span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {u.roles.length > 0
                        ? u.roles.map((r) =>
                            isKnownRole(r) ? (
                              <Badge key={r} variant={ROLE_META[r].variant}>
                                {t(ROLE_META[r].labelKey)}
                              </Badge>
                            ) : (
                              <Badge key={r} variant="outline">
                                {r}
                              </Badge>
                            ),
                          )
                        : "—"}
                    </div>
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">
                    {formatDate(u.created_at)}
                  </TableCell>
                </TableRow>
              ))}
              {filtered.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={3}
                    className="py-10 text-center text-sm text-muted-foreground"
                  >
                    {t("users.notFound")}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <Dialog
        open={createOpen}
        onOpenChange={(open) => {
          if (creating) return;
          setCreateOpen(open);
          if (!open) resetForm();
        }}
      >
        <DialogContent className="max-h-[85vh] max-w-[calc(100%-2rem)] overflow-y-auto sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("users.dialog.title")}</DialogTitle>
            <DialogDescription>{t("users.dialog.description")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="new-username">{t("users.dialog.username")}</Label>
              <Input
                id="new-username"
                className="w-full"
                autoComplete="off"
                placeholder="ivan.petrov"
                value={newUsername}
                onChange={(e) => setNewUsername(e.target.value)}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="new-password">{t("users.dialog.password")}</Label>
              <Input
                id="new-password"
                type="password"
                className="w-full"
                autoComplete="new-password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
              />
              <p
                className={`text-xs ${
                  newPassword.length > 0 && newPassword.length < MIN_PASSWORD
                    ? "text-destructive"
                    : "text-muted-foreground"
                }`}
              >
                {t("users.dialog.passwordHint", { count: MIN_PASSWORD })}
              </p>
            </div>
            <div className="grid gap-1.5">
              <Label>{t("users.dialog.role")}</Label>
              <Select value={newRole} onValueChange={(v) => setNewRole(v as Role)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(Object.keys(ROLE_META) as Role[]).map((r) => (
                    <SelectItem key={r} value={r}>
                      {t(ROLE_META[r].labelKey)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter className="gap-2 sm:justify-end">
            <Button variant="outline" onClick={closeDialog} disabled={creating}>
              {t("actions.cancel")}
            </Button>
            <Button onClick={() => void createUser()} disabled={!canSubmit || creating}>
              {creating ? t("users.dialog.submitting") : t("users.dialog.submit")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
