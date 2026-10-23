import { useState } from "react";
import { setToken } from "../auth";

// SignIn takes an API token (ADR-0039). It stays in this tab only.
export function SignIn({ error }: { error?: string }) {
  const [token, setValue] = useState("");
  return (
    <main className="mx-auto mt-24 max-w-md px-6">
      <h1 className="mb-2 text-2xl font-semibold">Sign in to Bohux</h1>
      <p className="mb-6 text-sm text-muted">
        Paste an API token. A viewer token with an expiry is enough to look around. It is kept in this tab only and
        forgotten when the tab closes.
      </p>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (token.trim() === "") return;
          setToken(token);
          window.location.assign("/ui/");
        }}
      >
        <label htmlFor="token" className="mb-1 block text-sm font-medium">API token</label>
        <input
          id="token"
          type="password"
          autoComplete="off"
          className="mb-4 w-full rounded border border-border bg-background px-3 py-2"
          value={token}
          onChange={(e) => setValue(e.target.value)}
        />
        {error && <p className="mb-4 text-sm text-danger" role="alert">{error}</p>}
        <button type="submit" className="rounded bg-accent px-4 py-2 text-white">Sign in</button>
      </form>
    </main>
  );
}
