import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useMutation } from "@connectrpc/connect-query";
import { DeploymentService } from "../gen/bohux/v1/deployment_pb";
import { ReleaseService } from "../gen/bohux/v1/release_pb";
import type { App } from "../gen/bohux/v1/app_pb";

const digestRef = /^[^@\s]+@sha256:[a-f0-9]{64}$/;

// key is a fresh idempotency key. getRandomValues works outside secure contexts too,
// where randomUUID does not exist.
function key(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return "web-" + Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

// AppActions deploys an image or rolls back, through the same RPCs as `bohux deploy
// --image` and `bohux rollback` (rule 8). Each asks before it acts: both change what
// runs (ToolPolicy DESTRUCTIVE in proto/).
export function AppActions({ app, project, env }: { app: App; project: string; env: string }) {
  const navigate = useNavigate();
  const [mode, setMode] = useState<"idle" | "deploy" | "rollback">("idle");
  const [image, setImage] = useState("");
  const [error, setError] = useState<string>();
  const createRelease = useMutation(ReleaseService.method.createRelease);
  const createDeployment = useMutation(DeploymentService.method.createDeployment);
  const rollback = useMutation(DeploymentService.method.rollback);
  const busy = createRelease.isPending || createDeployment.isPending || rollback.isPending;
  const follow = (id: string | undefined) => {
    if (id) void navigate({ to: "/p/$project/e/$env/d/$id", params: { project, env, id } });
  };

  const deploy = async () => {
    setError(undefined);
    const [repository, digest] = image.trim().split("@");
    try {
      const rel = await createRelease.mutateAsync({
        appId: app.meta?.id,
        from: { case: "image", value: { repository, digest } },
        idempotencyKey: key(),
      });
      const dep = await createDeployment.mutateAsync({
        releaseId: rel.release?.meta?.id,
        environmentId: app.environmentId,
        reason: "deployed from the web UI",
        idempotencyKey: key(),
      });
      follow(dep.deployment?.meta?.id);
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const rollBack = async () => {
    setError(undefined);
    try {
      const r = await rollback.mutateAsync({ appId: app.meta?.id, reason: "rolled back from the web UI", idempotencyKey: key() });
      follow(r.deployment?.meta?.id);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  if (mode === "deploy") {
    const valid = digestRef.test(image.trim());
    return (
      <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); if (valid) void deploy(); }}>
        <label className="sr-only" htmlFor={`img-${app.slug}`}>Image</label>
        <input id={`img-${app.slug}`} className="w-80 rounded border border-border bg-background px-2 py-1 text-xs"
          placeholder="repository@sha256:…" value={image} onChange={(e) => setImage(e.target.value)} />
        <button type="submit" disabled={!valid || busy} className="text-accent disabled:text-muted">
          Deploy {app.slug}
        </button>
        <button type="button" className="text-muted" onClick={() => setMode("idle")}>Cancel</button>
        {error && <span className="text-danger" role="alert">{error}</span>}
      </form>
    );
  }
  if (mode === "rollback") {
    return (
      <span className="flex flex-wrap items-center gap-2">
        Roll {app.slug} back to its last healthy release?
        <button disabled={busy} className="text-danger" onClick={() => void rollBack()}>Roll back</button>
        <button className="text-muted" onClick={() => setMode("idle")}>Cancel</button>
        {error && <span className="text-danger" role="alert">{error}</span>}
      </span>
    );
  }
  return (
    <span className="flex gap-3">
      <button className="text-accent" onClick={() => setMode("deploy")}>deploy</button>
      <button className="text-accent" onClick={() => setMode("rollback")}>rollback</button>
    </span>
  );
}
