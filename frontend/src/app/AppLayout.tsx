import { type ComponentType, Suspense } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Search,
  History,
  FileText,
  LayoutDashboard,
  Users,
  SlidersHorizontal,
  Gauge,
  FlaskConical,
  Layers,
  Target,
} from "lucide-react";
import { Spinner } from "@/shared/ui/Spinner";
import { Kicker } from "@/shared/ui/Kicker";
import { LanguageSwitcher } from "@/shared/ui/LanguageSwitcher";
import { TaskNotifications } from "@/features/task/ui/TaskNotifications";
import { RoleSwitcher } from "@/features/auth/ui/RoleSwitcher";
import { useRole } from "@/features/auth/RoleContext";
import { Role } from "@/shared/types";

// Подписи навигации живут в словарях (неймспейс app) — здесь только ключи,
// чтобы список секций оставался единственным источником структуры меню.
interface NavItem {
  to: string;
  labelKey:
    | "nav.search"
    | "nav.hypotheses"
    | "nav.topics"
    | "nav.goals"
    | "nav.history"
    | "nav.documents"
    | "nav.overview"
    | "nav.metrics"
    | "nav.users"
    | "nav.settings";
  icon: ComponentType<{
    className?: string;
  }>;
  end?: boolean;
}

interface NavGroup {
  labelKey: "groups.work" | "groups.corpus" | "groups.admin";
  items: NavItem[];
}

// Work navigation is shared by all roles; corpus and admin sections are added
// for operators and administrators.
const WORK: NavGroup = {
  labelKey: "groups.work",
  items: [
    { to: "/kpi", labelKey: "nav.goals", icon: Target },
    { to: "/hypotheses", labelKey: "nav.hypotheses", icon: FlaskConical },
    { to: "/", labelKey: "nav.search", icon: Search, end: true },
    { to: "/summary", labelKey: "nav.topics", icon: Layers },
    { to: "/history", labelKey: "nav.history", icon: History },
  ],
};

const CORPUS: NavGroup = {
  labelKey: "groups.corpus",
  items: [{ to: "/operator/documents", labelKey: "nav.documents", icon: FileText }],
};

const ADMIN: NavGroup = {
  labelKey: "groups.admin",
  items: [
    { to: "/admin", labelKey: "nav.overview", icon: LayoutDashboard, end: true },
    { to: "/admin/metrics", labelKey: "nav.metrics", icon: Gauge },
    { to: "/admin/users", labelKey: "nav.users", icon: Users },
    { to: "/admin/settings", labelKey: "nav.settings", icon: SlidersHorizontal },
  ],
};

const NAV: Record<
  Role,
  { subtitleKey: "subtitle.user" | "subtitle.operator" | "subtitle.admin"; groups: NavGroup[] }
> = {
  user: { subtitleKey: "subtitle.user", groups: [CORPUS, WORK] },
  operator: { subtitleKey: "subtitle.operator", groups: [CORPUS, WORK] },
  admin: { subtitleKey: "subtitle.admin", groups: [CORPUS, WORK, ADMIN] },
};

export function AppLayout() {
  const { role } = useRole();
  const { t, i18n } = useTranslation("app");
  const nav = NAV[role];
  const location = useLocation();
  const flat = nav.groups.flatMap((g) => g.items);
  return (
    // key={language}: смена языка перемонтирует дерево, так что переводятся и
    // строки, приходящие из чистых хелперов без подписки useTranslation.
    <div key={i18n.language} className="flex min-h-screen w-full bg-background">
      <aside className="sticky top-0 z-10 hidden h-screen w-64 shrink-0 flex-col border-r bg-sidebar md:flex print:hidden">
        <div className="flex items-center gap-2.5 px-4 pb-4 pt-5">
          <div className="flex size-9 items-center justify-center rounded-lg bg-primary text-primary-foreground">
            <FlaskConical className="size-4.5" aria-hidden />
          </div>
          <div className="min-w-0">
            <p className="font-display truncate text-[15px] leading-tight">{t("brand")}</p>
            <p className="truncate text-xs text-muted-foreground">{t(nav.subtitleKey)}</p>
          </div>
        </div>
        <nav className="flex-1 space-y-5 overflow-y-auto p-3" aria-label={t("groups.work")}>
          {nav.groups.map((group) => (
            <div key={group.labelKey}>
              <Kicker className="px-3 pb-1.5">{t(group.labelKey)}</Kicker>
              <div className="space-y-0.5">
                {group.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.end}
                    className={({ isActive }) =>
                      `flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
                        isActive
                          ? "bg-primary text-primary-foreground"
                          : "text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
                      }`
                    }
                  >
                    <item.icon className="size-4 shrink-0" aria-hidden />
                    {t(item.labelKey)}
                  </NavLink>
                ))}
              </div>
            </div>
          ))}
        </nav>
        <div className="border-t p-3">
          <RoleSwitcher />
        </div>
      </aside>

      {/* Mobile top bar */}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="hidden items-center justify-end gap-1 border-b bg-background px-4 py-2 md:flex print:hidden">
          <LanguageSwitcher />
          <TaskNotifications />
        </header>
        <header className="flex items-center justify-between border-b bg-background px-4 py-2 md:hidden print:hidden">
          <div className="flex items-center gap-2">
            <div className="flex size-7 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <FlaskConical className="size-4" aria-hidden />
            </div>
            <span className="font-display text-sm">{t("brand")}</span>
          </div>
          <div className="flex items-center gap-2">
            <TaskNotifications />
            <LanguageSwitcher />
            <div className="w-44">
              <RoleSwitcher />
            </div>
          </div>
        </header>
        <nav
          className="flex gap-1 overflow-x-auto border-b px-3 py-2 md:hidden"
          aria-label={t("groups.work")}
        >
          {flat.map((item) => {
            const active = item.end
              ? location.pathname === item.to
              : location.pathname.startsWith(item.to);
            return (
              <NavLink
                key={item.to}
                to={item.to}
                className={`whitespace-nowrap rounded-lg px-3 py-1.5 text-sm ${
                  active
                    ? "bg-primary font-medium text-primary-foreground"
                    : "text-muted-foreground"
                }`}
              >
                {t(item.labelKey)}
              </NavLink>
            );
          })}
        </nav>
        <main className="flex-1">
          <Suspense
            fallback={
              <div className="flex min-h-[40vh] items-center justify-center">
                <Spinner size="lg" />
              </div>
            }
          >
            <Outlet />
          </Suspense>
        </main>
      </div>
    </div>
  );
}
