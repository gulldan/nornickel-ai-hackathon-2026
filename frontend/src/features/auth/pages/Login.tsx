import { BookOpenText, LogIn } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Navigate, useLocation, useNavigate } from "react-router-dom";

import { Button } from "@/shared/ui/Button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/ui/Card";
import { Input } from "@/shared/ui/Input";
import { Label } from "@/shared/ui/Label";
import { useAuth } from "@/features/auth/AuthContext";
import { ApiError } from "@/shared/api/client";

// Роли демо-доступа: подписи и описания живут в словаре auth (login.accessRoles).
const ACCESS_ROLES = ["admin", "operator", "researcher"] as const;

export function Login() {
  const { t } = useTranslation("auth");
  const { auth, login } = useAuth();
  const navigate = useNavigate();
  const location = useLocation() as { state?: { from?: string } };
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (auth) {
    return <Navigate to={location.state?.from ?? "/"} replace />;
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await login(username.trim(), password);
      navigate(location.state?.from ?? "/", { replace: true });
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 401
          ? t("login.invalidCredentials")
          : t("login.failed"),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen w-full items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex items-center justify-center gap-2.5">
          <div className="flex size-9 items-center justify-center rounded-md bg-primary text-primary-foreground">
            <BookOpenText className="size-5" aria-hidden />
          </div>
          <div>
            <p className="text-base font-semibold leading-tight">Фабрика гипотез</p>
            <p className="text-xs text-muted-foreground">{t("login.subtitle")}</p>
          </div>
        </div>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("login.title")}</CardTitle>
          </CardHeader>
          <CardContent>
            <form className="space-y-4" onSubmit={(e) => void submit(e)}>
              <div className="space-y-1.5">
                <Label htmlFor="login-username">{t("login.username")}</Label>
                <Input
                  id="login-username"
                  autoFocus
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="login-password">{t("login.password")}</Label>
                <Input
                  id="login-password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
              </div>
              {error && (
                <p className="text-sm text-destructive" role="alert">
                  {error}
                </p>
              )}
              <Button type="submit" className="w-full" disabled={busy}>
                <LogIn className="size-4" aria-hidden />
                {busy ? t("login.signingIn") : t("login.signIn")}
              </Button>
            </form>
          </CardContent>
        </Card>
        <div className="mt-4 rounded-lg border border-dashed px-4 py-3">
          <p className="mb-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {t("login.accessTitle")}
          </p>
          <p className="mb-2 text-sm text-muted-foreground">{t("login.accessNote")}</p>
          <ul className="space-y-1 text-sm text-muted-foreground">
            {ACCESS_ROLES.map((key) => (
              <li key={key}>
                <span className="font-medium text-foreground">
                  {t(`login.accessRoles.${key}.role`)}
                </span>{" "}
                — {t(`login.accessRoles.${key}.detail`)}
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
}
