import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import {
  loadAuth,
  saveAuth,
  setUnauthorizedHandler,
  type TokenResponse,
} from "@/shared/api/client";
import { login as apiLogin } from "@/features/auth/api";
import type { Role } from "@/shared/types";

const KNOWN_ROLES: Role[] = ["user", "operator", "admin"];

interface AuthContextValue {
  auth: TokenResponse | null;
  /** Roles from the verified JWT, narrowed to the ones the UI knows. */
  roles: Role[];
  login: (username: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<TokenResponse | null>(() => loadAuth());

  const logout = useCallback(() => {
    saveAuth(null);
    setAuth(null);
  }, []);

  // Any 401 from the API drops the session back to the login screen.
  useEffect(() => {
    setUnauthorizedHandler(logout);
  }, [logout]);

  const login = useCallback(async (username: string, password: string) => {
    const t = await apiLogin(username, password);
    saveAuth(t);
    setAuth(t);
  }, []);

  const roles = useMemo<Role[]>(() => {
    const have = new Set(auth?.user.roles ?? []);
    return KNOWN_ROLES.filter((r) => have.has(r));
  }, [auth]);

  const value = useMemo(() => ({ auth, roles, login, logout }), [auth, roles, login, logout]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
