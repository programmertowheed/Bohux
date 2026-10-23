import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { Code } from "@connectrpc/connect";
import { ExecRequestSchema, ExecResponseSchema, type ExecResponse } from "./gen/bohux/v1/workload_pb";

// The web terminal's WebSocket bridge to WorkloadService.Exec (ADR-0046): the first
// frame is the API token as text, every later frame one binary protobuf message.
export const execSocketPath = "/sockets/v1/exec";

export function execSocketURL(loc: { protocol: string; host: string }): string {
  return (loc.protocol === "https:" ? "wss://" : "ws://") + loc.host + execSocketPath;
}

export function startFrame(appId: string, command: string[], rows: number, cols: number): Uint8Array {
  return toBinary(ExecRequestSchema, create(ExecRequestSchema, {
    msg: { case: "start", value: { appId, command, tty: true, size: { rows, cols } } },
  }));
}

export function stdinFrame(data: string): Uint8Array {
  return toBinary(ExecRequestSchema, create(ExecRequestSchema, {
    msg: { case: "stdin", value: new TextEncoder().encode(data) },
  }));
}

export function resizeFrame(rows: number, cols: number): Uint8Array {
  return toBinary(ExecRequestSchema, create(ExecRequestSchema, { msg: { case: "resize", value: { rows, cols } } }));
}

export function decodeFrame(data: ArrayBuffer): ExecResponse {
  return fromBinary(ExecResponseSchema, new Uint8Array(data));
}

// splitCommand splits on whitespace, as the shell would for a plain command line.
export function splitCommand(s: string): string[] {
  return s.trim().split(/\s+/).filter((x) => x !== "");
}

// closeMessage explains why the server ended a session: 1000 is a normal end, and
// 4000 + a Connect code is the error a Connect client would have got.
export function closeMessage(code: number, reason: string): string | undefined {
  if (code === 1000) return undefined;
  if (code > 4000 && code <= 4016) {
    return reason || (Code[code - 4000] ?? "error").replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase();
  }
  return reason || `connection closed (${code})`;
}

// mirrorStyles copies the text of every <style> element under the roots into a
// constructable stylesheet. xterm.js draws with <style> elements, which the UI's
// Content-Security-Policy refuses (ADR-0039); adopted stylesheets are not subject to
// it, so the policy stays as it is (ADR-0046). Start it before the terminal opens;
// it returns a function that stops it and removes the copies.
export function mirrorStyles(doc: Document, roots: Node[]): () => void {
  const sheets = new Map<HTMLStyleElement, CSSStyleSheet>();
  const adopt = (el: HTMLStyleElement) => {
    let sheet = sheets.get(el);
    if (!sheet) {
      sheet = new CSSStyleSheet();
      sheets.set(el, sheet);
      doc.adoptedStyleSheets = [...doc.adoptedStyleSheets, sheet];
    }
    sheet.replaceSync(el.textContent ?? "");
  };
  const drop = (el: HTMLStyleElement) => {
    const sheet = sheets.get(el);
    if (!sheet) return;
    sheets.delete(el);
    doc.adoptedStyleSheets = doc.adoptedStyleSheets.filter((s) => s !== sheet);
  };
  const visit = (n: Node, f: (el: HTMLStyleElement) => void) => {
    if (n instanceof HTMLStyleElement) f(n);
    else if (n instanceof Element) n.querySelectorAll("style").forEach(f);
  };
  const observer = new MutationObserver((records) => {
    for (const r of records) {
      r.addedNodes.forEach((n) => visit(n, adopt));
      r.removedNodes.forEach((n) => visit(n, drop));
      const target = r.target instanceof Text ? r.target.parentNode : r.target;
      if (target instanceof HTMLStyleElement && sheets.has(target)) adopt(target);
    }
  });
  for (const root of roots) {
    observer.observe(root, { childList: true, subtree: true, characterData: true });
  }
  return () => {
    observer.disconnect();
    for (const el of [...sheets.keys()]) drop(el);
  };
}
