// Shared transport for the gateway REST API. All calls go to /api/v1 on the
// SPA's own origin; in development Vite proxies that to the nginx gateway.
// Feature modules build their endpoints on request/postJSON/putJSON/authFetch.

import { i18n } from "@/shared/i18n";

const BASE = "/api/v1";
const STORAGE_KEY = "rag.auth";

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// ---- auth token (mirrors auth-service tokenResponse) ----

interface ApiUser {
  id: string;
  username: string;
  roles: string[];
}

export interface TokenResponse {
  access_token: string;
  refresh_token?: string;
  token_type: string;
  expires_at: string;
  user: ApiUser;
}

export function loadAuth(): TokenResponse | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as TokenResponse) : null;
  } catch {
    return null;
  }
}

export function saveAuth(auth: TokenResponse | null): void {
  if (auth) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(auth));
  } else {
    localStorage.removeItem(STORAGE_KEY);
  }
}

let onUnauthorized: (() => void) | null = null;

/** Registers the callback invoked when any call returns 401 (session ended). */
export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

// ---- transport ----

let refreshInFlight: Promise<boolean> | null = null;

async function tryRefresh(): Promise<boolean> {
  const auth = loadAuth();
  if (!auth?.refresh_token) return false;
  refreshInFlight ??= (async () => {
    try {
      const resp = await fetch(`${BASE}/auth/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ refresh_token: auth.refresh_token }),
      });
      if (!resp.ok) return false;
      saveAuth((await resp.json()) as TokenResponse);
      return true;
    } catch {
      return false;
    } finally {
      refreshInFlight = null;
    }
  })();
  return refreshInFlight;
}

/** Auth-aware fetch returning the raw Response. Handles 401 centrally: one
 *  silent refresh + retry, then the unauthorized handler. Callers that need a
 *  non-JSON body (e.g. file blobs) use this directly. */
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const doFetch = async (): Promise<Response> => {
    const auth = loadAuth();
    const headers = new Headers(init.headers);
    if (auth) headers.set("Authorization", `Bearer ${auth.access_token}`);
    return fetch(BASE + path, { ...init, headers });
  };

  let resp = await doFetch();
  if (resp.status === 401 && (await tryRefresh())) {
    resp = await doFetch();
  }
  if (resp.status === 401) {
    onUnauthorized?.();
    throw new ApiError(401, i18n.t("errors.sessionExpired"));
  }
  return resp;
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const resp = await authFetch(path, init);
  if (!resp.ok) {
    let msg = `${resp.status} ${resp.statusText}`;
    try {
      const body = (await resp.json()) as { error?: string };
      if (body.error) msg = body.error;
    } catch {
      // non-JSON error body; keep the status text
    }
    throw new ApiError(resp.status, msg);
  }
  if (resp.status === 204) return undefined as T;
  return (await resp.json()) as T;
}

export function postJSON<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function putJSON<T>(path: string, body: unknown): Promise<T> {
  return request<T>(path, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

/** WebSocket URL for the live ingestion-progress stream.
 *
 * In local Vite dev, `__GATEWAY__` is injected from `RAG_API` and points
 * straight to the backend gateway. In the Docker frontend image it is empty, so
 * the socket stays same-origin and the frontend nginx proxies the upgrade to
 * the backend gateway.
 */
export function wsURL(token: string): string {
  const gateway = __GATEWAY__ || window.location.origin;
  const gw = gateway.replace(/^http/, "ws").replace(/\/$/, "");
  return `${gw}${BASE}/ws?token=${encodeURIComponent(token)}`;
}
