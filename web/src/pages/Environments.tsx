import { Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { Failed, Loading, Table } from "../components/Table";
import { short } from "../format";
import { NewEnvironment } from "../components/Forms";
import { Channels } from "../components/Hooks";

export function Environments() {
  const { project } = useParams({ from: "/p/$project" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const envs = useQuery(EnvironmentService.method.listEnvironments, { projectId, pageSize: 100 },
    { enabled: projectId !== "" });
  if (p.error) return <Failed error={p.error} />;
  if (p.isPending || envs.isPending) return <Loading what="environments" />;
  if (envs.error) return <Failed error={envs.error} />;
  return (
    <section>
      <div className="mb-4 flex items-baseline gap-4">
        <h1 className="text-xl font-semibold">{project}</h1>
        <Link to="/p/$project/activity" params={{ project }} className="text-sm text-accent">activity</Link>
      </div>
      <Table
        head={["Environment", "Provider", "Node"]}
        empty="No environments yet. Create one with: bohux env create <slug> --node <node>"
        rows={envs.data.environments.map((e) => [
          <Link to="/p/$project/e/$env" params={{ project, env: e.slug }} className="text-accent">{e.slug}</Link>,
          e.providerBinding?.provider ?? "",
          e.providerBinding?.target.case === "nodeId" ? short(e.providerBinding.target.value) : "",
        ])}
      />
      <div className="mt-4"><NewEnvironment projectId={projectId} /></div>
      <div className="mt-8"><Channels projectId={projectId} /></div>
    </section>
  );
}
