import { useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { AuditService } from "../gen/bohux/v1/audit_pb";
import { PrincipalKind } from "../gen/bohux/v1/identity_pb";
import { Failed, Loading, Table } from "../components/Table";
import { enumName, short, when } from "../format";

// Activity is a project's audit trail, newest first (rule 10), as `bohux audit list -p`.
export function Activity() {
  const { project } = useParams({ from: "/p/$project/activity" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const q = useQuery(AuditService.method.listAuditEvents, { projectId, pageSize: 100 }, { enabled: projectId !== "" });
  const err = p.error ?? q.error;
  if (err) return <Failed error={err} />;
  if (!q.data) return <Loading what="activity" />;
  return (
    <section>
      <h1 className="mb-4 text-xl font-semibold">{project} activity</h1>
      <Table
        head={["When", "Who", "Action", "Resource"]}
        empty="Nothing has happened in this project yet."
        rows={q.data.auditEvents.map((e) => [
          when(e.occurredAt),
          `${enumName("PRINCIPAL_KIND_", PrincipalKind[e.actor?.kind ?? 0])} ${short(e.actor?.id)}`,
          e.action.replace("bohux.v1.", ""),
          `${(e.resource?.type ?? "").replace("bohux.v1.", "")} ${short(e.resource?.id)}`,
        ])}
      />
    </section>
  );
}
