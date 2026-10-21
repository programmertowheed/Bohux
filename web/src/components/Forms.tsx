import { useState, type ReactNode } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { NodeService } from "../gen/bohux/v1/node_pb";
import { AppService, type App } from "../gen/bohux/v1/app_pb";

// The forms below mirror `bohux env create`, `bohux app create`, `bohux scale`, and
// `bohux route add|remove` (rule 8): same RPCs, same checks on the server.

function key(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return "web-" + Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

const input = "rounded border border-border bg-background px-2 py-1 text-sm";

function Field({ id, label, children }: { id: string; label: string; children: ReactNode }) {
  return (
    <label htmlFor={id} className="flex flex-col gap-1 text-sm">
      <span className="text-muted">{label}</span>
      {children}
    </label>
  );
}

function Toggle({ label, children }: { label: string; children: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  if (!open) return <button className="text-sm text-accent" onClick={() => setOpen(true)}>{label}</button>;
  return (
    <div className="rounded border border-border p-4">
      {children(() => setOpen(false))}
    </div>
  );
}

export function NewEnvironment({ projectId }: { projectId: string }) {
  const nodes = useQuery(NodeService.method.listNodes, { pageSize: 100 });
  const create = useMutation(EnvironmentService.method.createEnvironment);
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [node, setNode] = useState("");
  const [error, setError] = useState<string>();
  return (
    <Toggle label="New environment">
      {(close) => (
        <form className="flex flex-wrap items-end gap-3" onSubmit={async (e) => {
          e.preventDefault();
          setError(undefined);
          try {
            await create.mutateAsync({ projectId, slug, idempotencyKey: key(),
              providerBinding: { provider: "docker", target: { case: "nodeId", value: node } } });
            await qc.invalidateQueries();
            close();
          } catch (err) {
            setError((err as Error).message);
          }
        }}>
          <Field id="env-slug" label="Slug"><input id="env-slug" className={input} value={slug} onChange={(e) => setSlug(e.target.value)} /></Field>
          <Field id="env-node" label="Node">
            <select id="env-node" className={input} value={node} onChange={(e) => setNode(e.target.value)}>
              <option value="">Choose a node</option>
              {nodes.data?.nodes.map((n) => <option key={n.meta?.id} value={n.meta?.id}>{n.slug}</option>)}
            </select>
          </Field>
          <button type="submit" disabled={!slug || !node || create.isPending} className="text-accent disabled:text-muted">Create</button>
          <button type="button" className="text-muted" onClick={close}>Cancel</button>
          {error && <p className="w-full text-sm text-danger" role="alert">{error}</p>}
        </form>
      )}
    </Toggle>
  );
}

export function NewApp({ environmentId }: { environmentId: string }) {
  const create = useMutation(AppService.method.createApp);
  const qc = useQueryClient();
  const [f, setF] = useState({ slug: "", image: "", port: "8080", health: "/", route: "", pull: "" });
  const [error, setError] = useState<string>();
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  return (
    <Toggle label="New app">
      {(close) => (
        <form className="grid grid-cols-2 gap-3 md:grid-cols-5" onSubmit={async (e) => {
          e.preventDefault();
          setError(undefined);
          const [repository, digest] = f.image.trim().split("@");
          const port = Number(f.port);
          try {
            await create.mutateAsync({
              environmentId, slug: f.slug, idempotencyKey: key(),
              source: { kind: { case: "image", value: { repository, digest } } },
              runtime: { replicas: 1, ports: port ? [{ containerPort: port }] : [],
                health: f.health && port ? { path: f.health, port } : undefined },
              routes: f.route && port ? [{ host: f.route.trim(), port }] : [],
              imagePullSecret: f.pull.trim(),
            });
            await qc.invalidateQueries();
            close();
          } catch (err) {
            setError((err as Error).message);
          }
        }}>
          <Field id="app-slug" label="Slug"><input id="app-slug" className={input} value={f.slug} onChange={set("slug")} /></Field>
          <Field id="app-image" label="Image (repository@sha256:…)"><input id="app-image" className={input} value={f.image} onChange={set("image")} /></Field>
          <Field id="app-port" label="Container port"><input id="app-port" className={input} value={f.port} onChange={set("port")} /></Field>
          <Field id="app-health" label="Health path"><input id="app-health" className={input} value={f.health} onChange={set("health")} /></Field>
          <Field id="app-route" label="Route host (optional)"><input id="app-route" className={input} value={f.route} onChange={set("route")} /></Field>
          <Field id="app-pull" label="Pull secret (optional)"><input id="app-pull" className={input} value={f.pull} onChange={set("pull")} /></Field>
          <div className="col-span-full flex gap-3">
            <button type="submit" disabled={!f.slug || !f.image || create.isPending} className="text-accent disabled:text-muted">Create</button>
            <button type="button" className="text-muted" onClick={close}>Cancel</button>
            {error && <span className="text-sm text-danger" role="alert">{error}</span>}
          </div>
        </form>
      )}
    </Toggle>
  );
}

// AppSettings scales the App and edits its routes, both without a deploy (ADR-0027,
// ADR-0034), and names the secrets its image is pulled and its Git source fetched
// with, for the next deploy or build (ADR-0041).
export function AppSettings({ app }: { app: App }) {
  const scale = useMutation(AppService.method.scaleApp);
  const update = useMutation(AppService.method.updateApp);
  const qc = useQueryClient();
  const [replicas, setReplicas] = useState(String(app.runtime?.replicas ?? 1));
  const [host, setHost] = useState("");
  const [pull, setPull] = useState(app.imagePullSecret);
  const git = app.source?.kind.case === "git" ? app.source.kind.value : undefined;
  const [deployKey, setDeployKey] = useState(git?.sshKeySecret ?? "");
  const [error, setError] = useState<string>();
  const port = app.runtime?.ports[0]?.containerPort ?? 0;
  const run = async (f: () => Promise<unknown>) => {
    setError(undefined);
    try {
      await f();
      await qc.invalidateQueries();
    } catch (err) {
      setError((err as Error).message);
    }
  };
  const save = (change: Partial<Pick<App, "routes" | "imagePullSecret" | "source">>) => run(() => update.mutateAsync({
    id: app.meta?.id, source: app.source, runtime: app.runtime, secrets: app.secrets,
    providerOptions: app.providerOptions, routes: app.routes, imagePullSecret: app.imagePullSecret,
    ...change, expectedRevisionId: app.meta?.revisionId, idempotencyKey: key(),
  }));
  const setRoutes = (routes: App["routes"]) => save({ routes });
  return (
    <Toggle label="settings">
      {(close) => (
        <div className="space-y-3 text-sm">
          <form className="flex items-end gap-2" onSubmit={(e) => {
            e.preventDefault();
            void run(() => scale.mutateAsync({ id: app.meta?.id, replicas: Number(replicas),
              expectedRevisionId: app.meta?.revisionId, idempotencyKey: key() }));
          }}>
            <Field id={`rep-${app.slug}`} label="Replicas">
              <input id={`rep-${app.slug}`} className={`${input} w-20`} value={replicas} onChange={(e) => setReplicas(e.target.value)} />
            </Field>
            <button type="submit" className="text-accent" disabled={scale.isPending}>Scale</button>
          </form>
          <ul className="space-y-1">
            {app.routes.map((r) => (
              <li key={r.host} className="flex gap-2">
                {r.host}:{r.port}
                <button className="text-danger" onClick={() => void setRoutes(app.routes.filter((x) => x.host !== r.host))}>remove</button>
              </li>
            ))}
          </ul>
          {port !== 0 && (
            <form className="flex items-end gap-2" onSubmit={(e) => {
              e.preventDefault();
              void setRoutes([...app.routes, { host: host.trim(), port } as App["routes"][number]]).then(() => setHost(""));
            }}>
              <Field id={`route-${app.slug}`} label={`Add a route to port ${port}`}>
                <input id={`route-${app.slug}`} className={input} value={host} onChange={(e) => setHost(e.target.value)} />
              </Field>
              <button type="submit" className="text-accent" disabled={!host || update.isPending}>Add</button>
            </form>
          )}
          <form className="flex items-end gap-2" onSubmit={(e) => {
            e.preventDefault();
            void save({ imagePullSecret: pull.trim() });
          }}>
            <Field id={`pull-${app.slug}`} label="Pull secret (empty: public image)">
              <input id={`pull-${app.slug}`} className={input} value={pull} onChange={(e) => setPull(e.target.value)} />
            </Field>
            <button type="submit" className="text-accent" disabled={pull.trim() === app.imagePullSecret || update.isPending}>Save</button>
          </form>
          {git && (
            <form className="flex items-end gap-2" onSubmit={(e) => {
              e.preventDefault();
              void save({ source: { ...app.source!, kind: { case: "git", value: { ...git, sshKeySecret: deployKey.trim() } } } });
            }}>
              <Field id={`key-${app.slug}`} label="Deploy key secret (empty: public repository)">
                <input id={`key-${app.slug}`} className={input} value={deployKey} onChange={(e) => setDeployKey(e.target.value)} />
              </Field>
              <button type="submit" className="text-accent" disabled={deployKey.trim() === git.sshKeySecret || update.isPending}>Save</button>
            </form>
          )}
          {error && <p className="text-danger" role="alert">{error}</p>}
          <button className="text-muted" onClick={close}>Close</button>
        </div>
      )}
    </Toggle>
  );
}
