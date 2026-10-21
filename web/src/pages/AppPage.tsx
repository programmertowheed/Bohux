import { useState } from "react";
import { Link, useParams } from "@tanstack/react-router";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { AppService } from "../gen/bohux/v1/app_pb";
import { ReleaseService, ReleaseState } from "../gen/bohux/v1/release_pb";
import { DeploymentService, DeploymentState } from "../gen/bohux/v1/deployment_pb";
import { SecretService } from "../gen/bohux/v1/secret_pb";
import { Failed, Loading, Table } from "../components/Table";
import { enumName, short, when } from "../format";
import { Webhooks } from "../components/Hooks";

function key(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return "web-" + Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

// AppPage is one App: its releases, its deployments, its Git push webhooks, and the
// environment's secrets it uses, like `bohux app show` plus the release, webhook, and
// secret commands.
export function AppPage() {
  const { project, env, app } = useParams({ from: "/p/$project/e/$env/a/$app" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const e = useQuery(EnvironmentService.method.getEnvironment,
    { lookup: { case: "slug", value: { projectId, slug: env } } }, { enabled: projectId !== "" });
  const envId = e.data?.environment?.meta?.id ?? "";
  const a = useQuery(AppService.method.getApp, { lookup: { case: "slug", value: { environmentId: envId, slug: app } } },
    { enabled: envId !== "" });
  const appId = a.data?.app?.meta?.id ?? "";
  const releases = useQuery(ReleaseService.method.listReleases, { appId, pageSize: 20 }, { enabled: appId !== "" });
  const deps = useQuery(DeploymentService.method.listDeployments, { environmentId: envId, appId, pageSize: 20 },
    { enabled: appId !== "", refetchInterval: 5000 });
  const err = p.error ?? e.error ?? a.error ?? releases.error ?? deps.error;
  if (err) return <Failed error={err} />;
  if (!a.data?.app || !releases.data || !deps.data) return <Loading what="the app" />;
  const used = new Set(a.data.app.secrets.map((s) => s.key));
  return (
    <section className="space-y-8">
      <h1 className="text-xl font-semibold">{project} / {env} / {app}</h1>
      <div>
        <h2 className="mb-2 font-semibold">Releases</h2>
        <Table
          head={["Release", "State", "Source", "Created"]}
          empty="No releases yet."
          rows={releases.data.releases.map((r) => [
            short(r.meta?.id),
            <span className={r.state === ReleaseState.FAILED ? "text-danger" : ""}>
              {enumName("RELEASE_STATE_", ReleaseState[r.state])}{r.artifactAvailable ? "" : " (image pruned)"}
            </span>,
            r.sourceRef,
            when(r.meta?.createdAt),
          ])}
        />
      </div>
      <div>
        <h2 className="mb-2 font-semibold">Deployments</h2>
        <Table
          head={["Deployment", "State", "Release", "Reason", "Started"]}
          empty="No deployments yet."
          rows={deps.data.deployments.map((d) => [
            <Link to="/p/$project/e/$env/d/$id" params={{ project, env, id: d.meta?.id ?? "" }} className="text-accent">
              {short(d.meta?.id)}
            </Link>,
            enumName("DEPLOYMENT_STATE_", DeploymentState[d.state]),
            short(d.releaseId),
            d.reason,
            when(d.meta?.createdAt),
          ])}
        />
      </div>
      {a.data.app.source?.kind.case === "git" && <Webhooks appId={appId} />}
      <Secrets environmentId={envId} used={used} />
    </section>
  );
}

// Secrets lists the environment's secrets (keys and handles only) and sets one. The
// value goes to the API once, over this origin, and is never shown again (rule 3).
function Secrets({ environmentId, used }: { environmentId: string; used: Set<string> }) {
  const list = useQuery(SecretService.method.listSecretRefs, { environmentId, pageSize: 100 });
  const set = useMutation(SecretService.method.setSecret);
  const qc = useQueryClient();
  const [k, setK] = useState("");
  const [v, setV] = useState("");
  const [error, setError] = useState<string>();
  const existing = list.data?.secrets.find((s) => s.key === k);
  return (
    <div>
      <h2 className="mb-2 font-semibold">Secrets of this environment</h2>
      <Table
        head={["Key", "Used by this app"]}
        empty="No secrets yet."
        rows={(list.data?.secrets ?? []).map((s) => [s.key, used.has(s.key) ? "yes" : ""])}
      />
      <form className="mt-3 flex flex-wrap items-end gap-2 text-sm" onSubmit={async (e) => {
        e.preventDefault();
        setError(undefined);
        try {
          await set.mutateAsync({ environmentId, key: k, value: v, expectedRevisionId: existing?.meta?.revisionId ?? "",
            idempotencyKey: key() });
          setV("");
          await qc.invalidateQueries();
        } catch (err) {
          setError((err as Error).message);
        }
      }}>
        <label className="flex flex-col gap-1"><span className="text-muted">Key</span>
          <input className="rounded border border-border bg-background px-2 py-1" value={k} onChange={(e) => setK(e.target.value)} />
        </label>
        <label className="flex flex-col gap-1"><span className="text-muted">Value</span>
          <input type="password" autoComplete="off" className="rounded border border-border bg-background px-2 py-1"
            value={v} onChange={(e) => setV(e.target.value)} />
        </label>
        <button type="submit" disabled={!k || !v || set.isPending} className="text-accent disabled:text-muted">
          {existing ? "Set a new value" : "Create"}
        </button>
        {error && <span className="text-danger" role="alert">{error}</span>}
      </form>
    </div>
  );
}
