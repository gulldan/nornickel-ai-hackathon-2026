// Authentication endpoints. Token storage + the bearer header live in the
// shared transport (shared/api/client); these just exchange credentials.
import { postJSON, type TokenResponse } from "@/shared/api/client";

export function login(username: string, password: string): Promise<TokenResponse> {
  return postJSON<TokenResponse>("/auth/login", { username, password });
}

export function register(
  username: string,
  password: string,
  roles?: string[],
): Promise<TokenResponse> {
  return postJSON<TokenResponse>("/auth/register", { username, password, roles });
}
