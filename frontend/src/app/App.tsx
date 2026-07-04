import { type ComponentType, lazy, type ReactNode } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { AppLayout } from "@/app/AppLayout";
import { ErrorBoundary, Forbidden, NotFound } from "@/app/ErrorPages";
import { AuthProvider, useAuth } from "@/features/auth/AuthContext";
import { Login } from "@/features/auth/pages/Login";
import { RoleProvider } from "@/features/auth/RoleContext";
import { TaskProvider } from "@/features/task/TaskContext";
import { Toaster } from "@/shared/ui/Sonner";
import { TooltipProvider } from "@/shared/ui/Tooltip";
import type { Role } from "@/shared/types";

// Every screen is loaded on demand so each route ships as its own chunk: a user
// never downloads operator/admin code, and the entry bundle stays small as
// features grow. The login screen is the one eager import (instant first paint).
function page<K extends string>(load: () => Promise<{ [P in K]: ComponentType }>, name: K) {
  return lazy(() => load().then((m) => ({ default: m[name] })));
}

const SearchHome = page(() => import("@/features/search/pages/SearchHome"), "SearchHome");
const SearchResults = page(() => import("@/features/search/pages/SearchResults"), "SearchResults");
const UserHistory = page(() => import("@/features/search/pages/UserHistory"), "UserHistory");
const Hypotheses = page(() => import("@/features/hypothesis/pages/Hypotheses"), "Hypotheses");
const HypothesisPage = page(
  () => import("@/features/hypothesis/pages/HypothesisPage"),
  "HypothesisPage",
);
const HypothesisReport = page(
  () => import("@/features/hypothesis/pages/HypothesisReport"),
  "HypothesisReport",
);
const HypothesisReportPage = page(
  () => import("@/features/hypothesis/pages/HypothesisReportPage"),
  "HypothesisReportPage",
);
const ClusterPage = page(() => import("@/features/cluster/pages/ClusterPage"), "ClusterPage");
const KpiPage = page(() => import("@/features/kpi/pages/KpiPage"), "KpiPage");
const SummaryPage = page(() => import("@/features/summary/pages/SummaryPage"), "SummaryPage");
const OperatorDocuments = page(
  () => import("@/features/document/pages/OperatorDocuments"),
  "OperatorDocuments",
);
const AdminDashboard = page(
  () => import("@/features/admin/pages/AdminDashboard"),
  "AdminDashboard",
);
const AdminUsers = page(() => import("@/features/admin/pages/AdminUsers"), "AdminUsers");
const AdminSettings = page(() => import("@/features/admin/pages/AdminSettings"), "AdminSettings");
const AdminMetrics = page(() => import("@/features/admin/pages/AdminMetrics"), "AdminMetrics");

/** Redirects anonymous visitors to /login, remembering where they came from. */
function RequireAuth({ children }: { children: ReactNode }) {
  const { auth } = useAuth();
  const location = useLocation();
  if (!auth) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}

/** Renders the 403 page unless the account holds one of the roles. */
function RequireRole({ roles, children }: { roles: Role[]; children: ReactNode }) {
  const { roles: have } = useAuth();
  if (!roles.some((r) => have.includes(r))) {
    return <Forbidden />;
  }
  return <>{children}</>;
}

const guard = (roles: Role[], element: ReactNode) => (
  <RequireRole roles={roles}>{element}</RequireRole>
);

export function App() {
  return (
    <AuthProvider>
      <RoleProvider>
        <TooltipProvider>
          <BrowserRouter>
            <ErrorBoundary>
              <Routes>
                <Route path="/login" element={<Login />} />
                <Route
                  element={
                    <RequireAuth>
                      <TaskProvider>
                        <AppLayout />
                      </TaskProvider>
                    </RequireAuth>
                  }
                >
                  <Route path="/" element={<SearchHome />} />
                  <Route path="/search" element={<SearchResults />} />
                  <Route path="/hypotheses" element={<Hypotheses />} />
                  <Route path="/hypotheses/report" element={<HypothesisReport />} />
                  <Route path="/hypotheses/:id/report" element={<HypothesisReportPage />} />
                  <Route path="/hypotheses/:id" element={<HypothesisPage />} />
                  <Route path="/directions/:id" element={<HypothesisPage />} />
                  <Route path="/clusters/:id" element={<ClusterPage />} />
                  <Route path="/summary" element={<SummaryPage />} />
                  <Route path="/kpi" element={<KpiPage />} />
                  <Route path="/history" element={<UserHistory />} />
                  <Route
                    path="/operator/documents"
                    element={guard(["user", "operator", "admin"], <OperatorDocuments />)}
                  />
                  <Route path="/admin" element={guard(["admin"], <AdminDashboard />)} />
                  <Route path="/admin/users" element={guard(["admin"], <AdminUsers />)} />
                  <Route path="/admin/settings" element={guard(["admin"], <AdminSettings />)} />
                  <Route
                    path="/admin/metrics"
                    element={guard(["admin", "operator"], <AdminMetrics />)}
                  />
                  <Route path="*" element={<NotFound />} />
                </Route>
              </Routes>
            </ErrorBoundary>
          </BrowserRouter>
          <Toaster position="bottom-right" />
        </TooltipProvider>
      </RoleProvider>
    </AuthProvider>
  );
}
