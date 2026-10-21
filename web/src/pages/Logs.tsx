import { useEffect, useRef, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { createClient } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { WorkloadService, LogStream, type LogLine } from "../gen/bohux/v1/workload_pb";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { AppService } from "../gen/bohux/v1/app_pb";
import { transport } from "../api";
import { Failed, Loading } from "../components/Table";
import { keepLast, stripANSI } from "../logs";

const workloads = createClient(WorkloadService, transport);
const maxLines = 2000;

// Logs follows an App's log lines, relayed from its node, like `bohux logs -f`.
export function Logs() {
  const { project, env, app } = useParams({ from: "/p/$project/e/$env/a/$app/logs" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const e = useQuery(EnvironmentService.method.getEnvironment,
    { lookup: { case: "slug", value: { projectId, slug: env } } }, { enabled: projectId !== "" });
  const envId = e.data?.environment?.meta?.id ?? "";
  const a = useQuery(AppService.method.getApp, { lookup: { case: "slug", value: { environmentId: envId, slug: app } } },
    { enabled: envId !== "" });
  const appId = a.data?.app?.meta?.id ?? "";
  const [lines, setLines] = useState<LogLine[]>([]);
  const [error, setError] = useState<Error>();
  const [paused, setPaused] = useState(false);
  const bottom = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (appId === "" || paused) return;
    const abort = new AbortController();
    (async () => {
      try {
        for await (const res of workloads.streamLogs({ appId, follow: true, tailLines: 200 }, { signal: abort.signal })) {
          setLines((l) => keepLast([...l, ...res.lines], maxLines));
          setError(undefined);
        }
      } catch (err) {
        if (!abort.signal.aborted) setError(err as Error);
      }
    })();
    return () => abort.abort();
  }, [appId, paused]);

  useEffect(() => {
    if (!paused) bottom.current?.scrollIntoView({ block: "end" });
  }, [lines, paused]);

  const err = p.error ?? e.error ?? a.error;
  if (err) return <Failed error={err} />;
  if (appId === "") return <Loading what="the app" />;
  return (
    <section className="space-y-3">
      <div className="flex items-center gap-4">
        <h1 className="text-xl font-semibold">{app} logs</h1>
        <button className="text-sm text-accent" onClick={() => setPaused((x) => !x)}>
          {paused ? "Follow" : "Pause"}
        </button>
        <button className="text-sm text-muted" onClick={() => setLines([])}>Clear</button>
        {error && <span className="text-sm text-danger" role="alert">{error.message}</span>}
      </div>
      <pre className="h-[70vh] overflow-auto rounded border border-border p-3 text-xs leading-5" aria-live="off">
        {lines.map((l, i) => (
          <div key={i} className={l.stream === LogStream.STDERR ? "text-danger" : ""}>
            <span className="text-muted">{l.instance} </span>
            {stripANSI(l.message)}
          </div>
        ))}
        <div ref={bottom} />
      </pre>
    </section>
  );
}
