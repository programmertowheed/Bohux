# ADR-0046: Web terminal — a WebSocket bridge to `WorkloadService.Exec`

- **Status:** proposed (transport chosen by the owner 2026-10-22, O29; the rest built on)
- **Date:** 2026-10-22
- **Deciders:** Towheed
- **Extends:** ADR-0039, ADR-0013

## Context
v0.2 lists "web terminal into a running container (UI over the existing `Exec` verb)".
`WorkloadService.Exec` is a bidirectional stream. Browsers cannot send a request body
while reading the response over HTTP/1.1, and `fetch` has no full-duplex streaming even
on HTTP/2, so Connect-Web cannot call it at all. The owner chose a WebSocket bridge on
the control plane over HTTP/2 end to end (O29). What is costly to change once clients
use it:

1. **The wire protocol** of the bridge: path, framing, how the caller authenticates.
2. **Whether the bridge is a second implementation of Exec** with its own permission
   check and audit, or the same one.
3. **The terminal emulator** in the UI, under a CSP that refuses inline styles
   (ADR-0039) — the reason O27 kept the log view a plain `<pre>`.
4. **A WebSocket library** in the control plane (a new dependency).

## Decision
1. **`GET /sockets/v1/exec`**, upgraded to a WebSocket on the API listener.
   - The **first frame is a text frame holding the API token**, exactly what the
     `Authorization: Bearer` header holds. Browsers cannot set headers on a WebSocket,
     and a token in the URL or in `Sec-WebSocket-Protocol` would reach access logs.
     The server waits 10 s for it.
   - **Every later frame is binary: one protobuf-encoded message**, `ExecRequest` from
     the client (the first must be `start`) and `ExecResponse` from the server. The
     bridge has no message types of its own; `proto/` stays the contract (rule 9).
   - **The server ends the session with a close frame**: 1000 after `exit`; otherwise
     `4000 + ` the Connect error code, with the same message a Connect client gets
     (`4007` permission denied, `4016` unauthenticated, `4014` node offline). A
     WebSocket has no half-close, so closing the socket ends the process's session,
     as cancelling the stream does for the CLI.
   - The server sends a ping every 30 s so idle sessions survive proxies, and refuses
     frames over 64 KiB.
   - Cross-origin upgrades are refused (the `Origin` host must equal `Host`), even
     though a token in the first frame already defeats cross-site WebSocket hijacking.
2. **One implementation.** The Exec handler's body takes any stream with `Receive` and
   `Send`; the Connect handler and the bridge both call it. The bridge authenticates
   through the auth interceptor's own check for `WorkloadService/Exec`, so the role
   (`deployer`), the project scope, and the audit event are the ones the CLI gets.
   This is a transport for a capability the CLI already has (`bohux exec -it`), so
   rule 8 holds: the UI gains nothing the CLI lacks.
3. **xterm.js** (`@xterm/xterm`, `@xterm/addon-fit`) is the emulator. Its renderer
   writes `<style>` elements, which `style-src 'self'` refuses. The UI copies each of
   those elements' text into a constructable stylesheet (`adoptedStyleSheets`), which
   CSP does not govern, so **the CSP stays as ADR-0039 set it**. The text comes from
   xterm.js and the UI's theme, never from terminal output. The browser still logs the
   refused inline styles to the console.
4. **`github.com/coder/websocket` v1.8.14 or later** (ISC licence, no dependencies,
   `context`-aware; already in the module graph). v1.8.12 answers the client's close
   frame with a second one after the server has sent its own, which Chromium reports
   as an abnormal close (1006) and loses the exit status.

## Alternatives considered
- **HTTP/2 end to end through the ingress.** No new endpoint, but browsers still lack
  full-duplex `fetch`, so it would not work at all today. Lost (O29).
- **A bridge that dials the control plane's own Connect endpoint.** One code path in
  theory, but a loopback HTTP/2 client per session inside the server, and the token
  replayed as a header. Lost to calling the handler body directly.
- **Token in the query string or `Sec-WebSocket-Protocol`.** Simpler client, but both
  reach proxy and access logs (rule 3 spirit). Lost.
- **A JSON framing (protojson).** Readable in devtools, but stdin and stdout are bytes;
  base64 in JSON costs a third more and a second encoding. Lost.
- **Allowing `style-src 'unsafe-inline'` or a nonce for xterm.js** (O27's options).
  Weakens or complicates the policy for the whole UI. Lost to adopted stylesheets.
- **xterm.js's WebGL renderer** to avoid its DOM styles. Still writes one `<style>`,
  and WebGL is missing on some machines. Lost.
- **`gorilla/websocket`.** Mature, but no `context` support and concurrent writes need
  external locking. Lost on fit; either would do.

## Consequences
- Easy: a Terminal page per App in the UI; the same exec audit trail for both clients.
  The log view could now use xterm.js too (O27) without changing the CSP.
- Hard: a second network path to Exec to keep in step with the Connect one; the bridge
  path must be routed wherever the API is (it is: same listener, same host). Two npm
  dependencies (~300 KiB, loaded only on the Terminal page) and one Go dependency.
- The adopted-stylesheet copy depends on xterm.js keeping its styles in `<style>`
  elements. Checked in Chromium against an installed control plane (colors, resize,
  exit status); there is no browser test in CI yet, so an xterm.js upgrade needs that
  check repeated by hand.

## Revisit when
Browsers ship full-duplex `fetch` streaming (then Connect-Web can call Exec directly),
or xterm.js gains CSP nonce support.
