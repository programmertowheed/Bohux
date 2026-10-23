import { Link } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { Failed, Loading, Table } from "../components/Table";

export function Projects() {
  const q = useQuery(ProjectService.method.listProjects, { pageSize: 100 });
  if (q.isPending) return <Loading what="projects" />;
  if (q.error) return <Failed error={q.error} />;
  return (
    <section>
      <h1 className="mb-4 text-xl font-semibold">Projects</h1>
      <Table
        head={["Project", "Name"]}
        empty="No projects yet. Create one with: bohux project create <slug>"
        rows={q.data.projects.map((p) => [
          <Link to="/p/$project" params={{ project: p.slug }} className="text-accent">{p.slug}</Link>,
          p.displayName,
        ])}
      />
    </section>
  );
}
