import { useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { IdentityService, Role } from "../gen/bohux/v1/identity_pb";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { Failed, Loading, Table } from "../components/Table";
import { Field, Toggle, input, key } from "../components/Forms";
import { Once } from "../components/Hooks";
import { enumName, short, when } from "../format";

const lifetimes: [string, number][] = [["No expiry", 0], ["1 day", 1], ["30 days", 30], ["90 days", 90]];

// Tokens is `bohux token list|create|revoke` (rule 8): who can call the API, with which
// role, in which project. A new token's secret is shown once, as the CLI prints it once
// (ADR-0022); the UI keeps it in this page's state only.
export function Tokens() {
  const list = useQuery(IdentityService.method.listApiTokens, { pageSize: 100 });
  const projects = useQuery(ProjectService.method.listProjects, { pageSize: 100 });
  const create = useMutation(IdentityService.method.createApiToken);
  const revoke = useMutation(IdentityService.method.revokeApiToken);
  const qc = useQueryClient();
  const [f, setF] = useState({ name: "", role: Role.VIEWER, project: "", days: 30 });
  const [secret, setSecret] = useState<string>();
  const [error, setError] = useState<string>();
  if (list.error) return <Failed error={list.error} />;
  if (!list.data) return <Loading what="tokens" />;
  const slug = (id: string) => (id ? projects.data?.projects.find((p) => p.meta?.id === id)?.slug ?? short(id) : "all projects");
  return (
    <section className="space-y-4">
      <h1 className="text-xl font-semibold">API tokens</h1>
      <Table
        head={["Token", "Name", "Project", "Role", "Expires", "Last used", ""]}
        empty="No tokens."
        rows={list.data.apiTokens.map((t) => [
          short(t.meta?.id),
          t.name,
          slug(t.projectId),
          enumName("ROLE_", Role[t.role]),
          t.expiresAt ? when(t.expiresAt) : "never",
          t.lastUsedAt ? when(t.lastUsedAt) : "",
          t.revokedAt ? <span className="text-muted">revoked {when(t.revokedAt)}</span> : (
            <button className="text-danger" onClick={async () => {
              if (!window.confirm(`Revoke ${t.name}? Anything using it stops working at once.`)) return;
              setError(undefined);
              try {
                await revoke.mutateAsync({ id: t.meta?.id, expectedRevisionId: t.meta?.revisionId, idempotencyKey: key() });
                await qc.invalidateQueries();
              } catch (err) {
                setError((err as Error).message);
              }
            }}>revoke</button>
          ),
        ])}
      />
      {secret && <Once label="The new token" value={secret} onDone={() => setSecret(undefined)} />}
      <Toggle label="New token">
        {(close) => (
          <form className="flex flex-wrap items-end gap-3" onSubmit={async (e) => {
            e.preventDefault();
            setError(undefined);
            try {
              const r = await create.mutateAsync({
                name: f.name.trim(), role: f.role, projectId: f.project, idempotencyKey: key(),
                expiresAt: f.days ? timestampFromDate(new Date(Date.now() + f.days * 86_400_000)) : undefined,
              });
              setSecret(r.secret);
              setF({ ...f, name: "" });
              await qc.invalidateQueries();
              close();
            } catch (err) {
              setError((err as Error).message);
            }
          }}>
            <Field id="tok-name" label="Name">
              <input id="tok-name" className={input} value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
            </Field>
            <Field id="tok-role" label="Role">
              <select id="tok-role" className={input} value={f.role} onChange={(e) => setF({ ...f, role: Number(e.target.value) })}>
                <option value={Role.VIEWER}>viewer</option>
                <option value={Role.DEPLOYER}>deployer</option>
                <option value={Role.OWNER}>owner</option>
              </select>
            </Field>
            <Field id="tok-project" label="Project">
              <select id="tok-project" className={input} value={f.project} onChange={(e) => setF({ ...f, project: e.target.value })}>
                <option value="">All projects</option>
                {projects.data?.projects.map((p) => <option key={p.meta?.id} value={p.meta?.id}>{p.slug}</option>)}
              </select>
            </Field>
            <Field id="tok-expires" label="Expires">
              <select id="tok-expires" className={input} value={f.days} onChange={(e) => setF({ ...f, days: Number(e.target.value) })}>
                {lifetimes.map(([label, days]) => <option key={days} value={days}>{label}</option>)}
              </select>
            </Field>
            <button type="submit" disabled={!f.name.trim() || create.isPending} className="text-accent disabled:text-muted">Create</button>
            <button type="button" className="text-muted" onClick={close}>Cancel</button>
          </form>
        )}
      </Toggle>
      {error && <p className="text-sm text-danger" role="alert">{error}</p>}
    </section>
  );
}
