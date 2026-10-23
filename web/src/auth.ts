// The API token lives in this tab only (ADR-0039): sessionStorage, never
// localStorage, a cookie, or a URL. Closing the tab signs out.
const key = "bohux.token";

export function getToken(): string | null {
  try {
    return sessionStorage.getItem(key);
  } catch {
    return null;
  }
}

export function setToken(token: string): void {
  sessionStorage.setItem(key, token.trim());
}

export function clearToken(): void {
  sessionStorage.removeItem(key);
}
