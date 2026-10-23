import { useEffect, useRef, useState } from "react";
import { useParams } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { ProjectService } from "../gen/bohux/v1/project_pb";
import { EnvironmentService } from "../gen/bohux/v1/environment_pb";
import { AppService } from "../gen/bohux/v1/app_pb";
import { getToken } from "../auth";
import { Failed, Loading } from "../components/Table";
import {
  closeMessage, decodeFrame, execSocketURL, mirrorStyles, resizeFrame, splitCommand, startFrame, stdinFrame,
} from "../terminal";

type Session = { command: string[]; id: number };

// Terminal opens a shell in one of the App's instances, like `bohux exec -it`, over
// the API's WebSocket bridge to Exec (ADR-0046). It needs the deployer role.
export function Terminal() {
  const { project, env, app } = useParams({ from: "/p/$project/e/$env/a/$app/terminal" });
  const p = useQuery(ProjectService.method.getProject, { lookup: { case: "slug", value: project } });
  const projectId = p.data?.project?.meta?.id ?? "";
  const e = useQuery(EnvironmentService.method.getEnvironment,
    { lookup: { case: "slug", value: { projectId, slug: env } } }, { enabled: projectId !== "" });
  const envId = e.data?.environment?.meta?.id ?? "";
  const a = useQuery(AppService.method.getApp, { lookup: { case: "slug", value: { environmentId: envId, slug: app } } },
    { enabled: envId !== "" });
  const appId = a.data?.app?.meta?.id ?? "";
  const [command, setCommand] = useState("sh");
  const [session, setSession] = useState<Session>();
  const [status, setStatus] = useState<{ text: string; error?: boolean }>();

  const err = p.error ?? e.error ?? a.error;
  if (err) return <Failed error={err} />;
  if (appId === "") return <Loading what="the app" />;
  const connect = () => {
    const argv = splitCommand(command);
    if (argv.length === 0) return;
    setStatus(undefined);
    setSession({ command: argv, id: (session?.id ?? 0) + 1 });
  };
  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">{app} terminal</h1>
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted">Command</span>
          <input
            className="rounded border border-border bg-transparent px-2 py-1 font-mono"
            value={command}
            onChange={(ev) => setCommand(ev.target.value)}
            onKeyDown={(ev) => ev.key === "Enter" && !session && connect()}
            disabled={session !== undefined}
          />
        </label>
        {session
          ? <button className="text-sm text-accent" onClick={() => setSession(undefined)}>Disconnect</button>
          : <button className="text-sm text-accent" onClick={connect}>Connect</button>}
        {status && <span className={status.error ? "text-sm text-danger" : "text-sm text-muted"} role="status">{status.text}</span>}
      </div>
      {session && (
        <Screen
          key={session.id}
          appId={appId}
          command={session.command}
          onEnd={(text, error) => {
            setStatus({ text, error });
            setSession(undefined);
          }}
        />
      )}
      <p className="text-xs text-muted">
        Runs in any instance of the app. Every session is recorded in the activity log; its input and output are not.
      </p>
    </section>
  );
}

// Screen is one session: an xterm.js terminal wired to the socket until either ends.
function Screen({ appId, command, onEnd }: { appId: string; command: string[]; onEnd: (text: string, error?: boolean) => void }) {
  const box = useRef<HTMLDivElement>(null);
  const ended = useRef(onEnd);
  ended.current = onEnd;

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const stopMirror = mirrorStyles(document, [el, document.head]);
    const term = new XTerm({ cursorBlink: true, fontSize: 13, convertEol: false });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();
    let exitCode: number | undefined;
    let closing = false;
    const ws = new WebSocket(execSocketURL(window.location));
    ws.binaryType = "arraybuffer";
    const send = (b: Uint8Array) => ws.readyState === WebSocket.OPEN && ws.send(b);
    ws.onopen = () => {
      ws.send(getToken() ?? "");
      ws.send(startFrame(appId, command, term.rows, term.cols));
      term.focus();
    };
    ws.onmessage = (ev) => {
      const m = decodeFrame(ev.data as ArrayBuffer);
      switch (m.msg.case) {
        case "stdout":
        case "stderr":
          term.write(m.msg.value);
          break;
        case "exit":
          exitCode = m.msg.value.exitCode;
          break;
      }
    };
    ws.onclose = (ev) => {
      if (closing) return;
      const why = closeMessage(ev.code, ev.reason);
      if (why) ended.current(why, true);
      else ended.current(exitCode === undefined ? "Session ended." : `Process exited with status ${exitCode}.`);
    };
    const input = term.onData((d) => send(stdinFrame(d)));
    const resize = term.onResize(({ rows, cols }) => send(resizeFrame(rows, cols)));
    const onWindow = () => fit.fit();
    window.addEventListener("resize", onWindow);
    return () => {
      closing = true;
      window.removeEventListener("resize", onWindow);
      input.dispose();
      resize.dispose();
      ws.close();
      term.dispose();
      stopMirror();
    };
  }, [appId, command]);

  return <div ref={box} className="h-[70vh] rounded border border-border bg-black p-2" />;
}
