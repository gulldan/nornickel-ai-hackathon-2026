import { Check, ChevronsUpDown, LogOut } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import { useAuth } from "@/features/auth/AuthContext";
import { ROLE_META, useRole } from "@/features/auth/RoleContext";
import type { Role } from "@/shared/types";
import { Avatar, AvatarFallback } from "@/shared/ui/Avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/shared/ui/DropdownMenu";

function initialsOf(username: string): string {
  return username.slice(0, 2).toUpperCase();
}

export function RoleSwitcher() {
  const { t } = useTranslation("auth");
  const { auth, logout } = useAuth();
  const { role, available, setRole } = useRole();
  const navigate = useNavigate();
  const meta = ROLE_META[role];
  const username = auth?.user.username ?? "—";

  const handleSelect = (next: Role) => {
    if (next === role) return;
    setRole(next);
    navigate(ROLE_META[next].home);
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="flex w-full items-center gap-2 rounded-md p-2 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <Avatar size="sm">
          <AvatarFallback>{initialsOf(username)}</AvatarFallback>
        </Avatar>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm font-medium">{username}</span>
          <span className="truncate text-xs text-muted-foreground">{meta.label}</span>
        </span>
        <ChevronsUpDown className="size-4 shrink-0 text-muted-foreground" aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-56">
        {available.length > 1 && (
          <>
            <DropdownMenuLabel>{t("switcher.viewMode")}</DropdownMenuLabel>
            <DropdownMenuSeparator />
            {available.map((r) => (
              <DropdownMenuItem key={r} onClick={() => handleSelect(r)}>
                <span className="flex-1">{ROLE_META[r].label}</span>
                {r === role && <Check className="size-4" aria-hidden />}
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem onClick={logout}>
          <LogOut className="size-4" aria-hidden />
          <span className="flex-1">{t("switcher.logout")}</span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
