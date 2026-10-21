import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { AppService } from "../gen/bohux/v1/app_pb";
import { DeploymentService, DeploymentState } from "../gen/bohux/v1/deployment_pb";
import { Failed, Loading, Table } from "../components/Table";
import { enumName, short, when } from "../format";

const stateColor: Partial<Record<DeploymentState, string>> = {
  [DeploymentState.SUCCEEDED]: "text-ok",
  [DeploymentState.FAILED]: "text-danger",
  [DeploymentState.ROLLED_BACK]: "text-danger",
};

export function Environment() {
  const { project, env } = useParams({ from: "/p/$project/e/$env" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const e = useQuery(EnvironmentService.method.getEnvironment,
    { lookup: { case: "slug", value: { projectId, slug: env } } }, { enabled: projectId !== "" });
  const envId = e.data?.environment?.meta?.id ?? "";
  const apps = useQuery(AppService.method.listApps, { environmentId: envId, pageSize: 100 }, { enabled: envId !== "" });
  const deps = useQuery(DeploymentService.method.listDeployments, { environmentId: envId, pageSize: 25 },
    { enabled: envId !== "", refetchInterval: 5000 });
  const err = p.error ?? e.error ?? apps.error ?? deps.error;
  if (err) return <Failed error={err} />;
  if (!apps.data || !deps.data) return <Loading what="the environment" />;
  const slugOf = new Map(apps.data.apps.map((a) => [a.meta?.id ?? "", a.slug]));
  return (
    <section className="space-y-8">
      <h1 className="text-xl font-semibold">{project} / {env}</h1>
      <div>
        <h2 className="mb-2 font-semibold">Apps</h2>
        <Table
          head={["App", "Replicas", "Routes", "Source"]}
          empty="No apps yet. Create one with: bohux app create <slug> -e <env> --image …"
          rows={apps.data.apps.map((a) => [
            a.slug,
            String(a.runtime?.replicas ?? 0),
            a.routes.map((r) => r.host).join(", "),
            a.source?.kind.case === "image"
              ? `${a.source.kind.value.repository}@${a.source.kind.value.digest.slice(0, 19)}…`
              : a.source?.kind.case === "git" ? a.source.kind.value.repoUrl : "",
          ])}
        />
      </div>
      <div>
        <h2 className="mb-2 font-semibold">Recent deployments</h2>
        <Table
          head={["Deployment", "State", "Release", "Started"]}
          empty="No deployments yet."
          rows={deps.data.deployments.map((d) => [
            <Link to="/p/$project/e/$env/d/$id" params={{ project, env, id: d.meta?.id ?? "" }} className="text-accent">
              {short(d.meta?.id)}
            </Link>,
            <span className={stateColor[d.state] ?? ""}>{enumName("DEPLOYMENT_STATE_", DeploymentState[d.state])}</span>,
            short(d.releaseId),
            when(d.meta?.createdAt),
          ])}
        />
        <p className="mt-2 text-xs text-muted">Apps: {[...slugOf.values()].join(", ")}. Refreshes every 5 s.</p>
      </div>
    </section>
  );
}
