import { useEffect, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { createClient } from "@connectrpc/connect";
import { DeploymentService, DeploymentState, type Deployment as Dep } from "../gen/bohux/v1/deployment_pb";
import { transport } from "../api";
import { Failed, Loading } from "../components/Table";
import { enumName, short, when } from "../format";

const deployments = createClient(DeploymentService, transport);

const terminal = new Set([
  DeploymentState.SUCCEEDED,
  DeploymentState.FAILED,
  DeploymentState.ROLLED_BACK,
  DeploymentState.CANCELLED,
]);

type Step = { state: DeploymentState; at: Date };

// useWatch follows a Deployment over WatchDeployment until it ends, reconnecting
// after a dropped stream as the CLI does: the run goes on whether anyone watches.
export function useWatch(id: string) {
  const [dep, setDep] = useState<Dep>();
  const [steps, setSteps] = useState<Step[]>([]);
  const [error, setError] = useState<Error>();
  useEffect(() => {
    const abort = new AbortController();
    let last: DeploymentState | undefined;
    (async () => {
      for (let attempt = 0; !abort.signal.aborted; attempt++) {
        try {
          for await (const res of deployments.watchDeployment({ id }, { signal: abort.signal })) {
            const d = res.deployment;
            if (!d) continue;
            setDep(d);
            setError(undefined);
            if (d.state !== last) {
              last = d.state;
              setSteps((s) => [...s, { state: d.state, at: new Date() }]);
            }
            if (terminal.has(d.state)) return;
          }
          if (last !== undefined && terminal.has(last)) return;
        } catch (e) {
          if (abort.signal.aborted) return;
          setError(e as Error);
        }
        await new Promise((r) => setTimeout(r, Math.min(1000 * 2 ** attempt, 10000)));
      }
    })();
    return () => abort.abort();
  }, [id]);
  return { dep, steps, error };
}

export function Deployment() {
  const { id } = useParams({ from: "/p/$project/e/$env/d/$id" });
  const { dep, steps, error } = useWatch(id);
  if (!dep && error) return <Failed error={error} />;
  if (!dep) return <Loading what="the deployment" />;
  const done = terminal.has(dep.state);
  return (
    <section className="space-y-6">
      <h1 className="text-xl font-semibold">Deployment {short(dep.meta?.id)}</h1>
      <dl className="grid grid-cols-[10rem_1fr] gap-y-1 text-sm">
        <dt className="text-muted">State</dt>
        <dd aria-live="polite">{enumName("DEPLOYMENT_STATE_", DeploymentState[dep.state])}{done ? "" : " …"}</dd>
        <dt className="text-muted">Release</dt>
        <dd>{short(dep.releaseId)}</dd>
        {dep.rollbackOf && (<><dt className="text-muted">Rollback of</dt><dd>{short(dep.rollbackOf)}</dd></>)}
        {dep.reason && (<><dt className="text-muted">Reason</dt><dd>{dep.reason}</dd></>)}
        {dep.failure?.reason && (<><dt className="text-muted">Failure</dt><dd className="text-danger">{dep.failure.reason}</dd></>)}
        <dt className="text-muted">Started</dt>
        <dd>{when(dep.startedAt ?? dep.meta?.createdAt)}</dd>
        {dep.finishedAt && (<><dt className="text-muted">Finished</dt><dd>{when(dep.finishedAt)}</dd></>)}
      </dl>
      {steps.length > 0 && (
        <div>
          <h2 className="mb-2 font-semibold">Seen here</h2>
          <ol className="space-y-1 text-sm">
            {steps.map((s, i) => (
              <li key={i}>
                <span className="text-muted">{s.at.toLocaleTimeString()}</span>{" "}
                {enumName("DEPLOYMENT_STATE_", DeploymentState[s.state])}
              </li>
            ))}
          </ol>
        </div>
      )}
      {error && !done && <p className="text-sm text-muted" role="status">Reconnecting: {error.message}</p>}
    </section>
  );
}
