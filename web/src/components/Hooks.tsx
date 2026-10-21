import { useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { WebhookFlavor, WebhookService } from "../gen/bohux/v1/webhook_pb";
import { NotificationKind, NotificationService } from "../gen/bohux/v1/notification_pb";
import { DeploymentState } from "../gen/bohux/v1/deployment_pb";
import { Table } from "./Table";
import { Field, Toggle, input, key } from "./Forms";
import { enumName, short } from "../format";

// These mirror `bohux webhook list|create|remove` and `bohux channel list|create|remove`
// (rule 8). A webhook's secret and a channel's signing secret are shown once, right
// after creation, as the CLI prints them once; the UI keeps them in this component's
// state only. A channel's URL is sent once and never shown back (ADR-0038).

// Once shows a secret the API returns a single time.
export function Once({ label, value, onDone }: { label: string; value: string; onDone: () => void }) {
  return (
    <div className="rounded border border-border p-3 text-sm" role="status">
      <p className="text-muted">{label}. It is shown only now: copy it before you close this.</p>
      <code className="block break-all py-2">{value}</code>
      <button className="text-accent" onClick={onDone}>Done</button>
    </div>
  );
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// Webhooks lists an App's Git push webhooks, creates, and removes them (ADR-0037).
export function Webhooks({ appId }: { appId: string }) {
  const list = useQuery(WebhookService.method.listWebhooks, { appId, pageSize: 100 });
  const create = useMutation(WebhookService.method.createWebhook);
  const remove = useMutation(WebhookService.method.deleteWebhook);
  const qc = useQueryClient();
  const [branch, setBranch] = useState("main");
  const [flavor, setFlavor] = useState(WebhookFlavor.GITHUB);
  const [secret, setSecret] = useState<string>();
  const [error, setError] = useState<string>();
  const url = (path: string) => window.location.origin + path;
  return (
    <div>
      <h2 className="mb-2 font-semibold">Git push webhooks</h2>
      <Table
        head={["Webhook", "Branch", "Flavor", "Payload URL", ""]}
        empty="No webhooks. A push to a watched branch builds and deploys it."
        rows={(list.data?.webhooks ?? []).map((w) => [
          short(w.meta?.id),
          w.branch,
          enumName("WEBHOOK_FLAVOR_", WebhookFlavor[w.flavor]),
          <code className="break-all">{url(w.path)}</code>,
          <button className="text-danger" onClick={async () => {
            if (!window.confirm(`Remove the webhook for ${w.branch}? Pushes stop deploying.`)) return;
            setError(undefined);
            try {
              await remove.mutateAsync({ id: w.meta?.id, expectedRevisionId: w.meta?.revisionId, idempotencyKey: key() });
              await qc.invalidateQueries();
            } catch (err) {
              setError(errorText(err));
            }
          }}>remove</button>,
        ])}
      />
      {secret && <div className="mt-3"><Once label="The webhook's secret (paste it into your Git host)" value={secret}
        onDone={() => setSecret(undefined)} /></div>}
      <div className="mt-3">
        <Toggle label="New webhook">
          {(close) => (
            <form className="flex flex-wrap items-end gap-3" onSubmit={async (e) => {
              e.preventDefault();
              setError(undefined);
              try {
                const r = await create.mutateAsync({ appId, branch: branch.trim(), flavor, idempotencyKey: key() });
                setSecret(r.secret);
                await qc.invalidateQueries();
                close();
              } catch (err) {
                setError(errorText(err));
              }
            }}>
              <Field id="hook-branch" label="Branch">
                <input id="hook-branch" className={input} value={branch} onChange={(e) => setBranch(e.target.value)} />
              </Field>
              <Field id="hook-flavor" label="Git host">
                <select id="hook-flavor" className={input} value={flavor} onChange={(e) => setFlavor(Number(e.target.value))}>
                  <option value={WebhookFlavor.GITHUB}>GitHub, Gitea</option>
                  <option value={WebhookFlavor.GENERIC}>Generic</option>
                </select>
              </Field>
              <button type="submit" disabled={!branch.trim() || create.isPending} className="text-accent disabled:text-muted">Create</button>
              <button type="button" className="text-muted" onClick={close}>Cancel</button>
            </form>
          )}
        </Toggle>
      </div>
      {error && <p className="mt-2 text-sm text-danger" role="alert">{error}</p>}
    </div>
  );
}

// target is what CreateNotificationChannel takes: the URL, or for email a mailto:
// list, from "a@x, b@y" as `bohux channel create --kind email` accepts (ADR-0043).
function target(kind: NotificationKind, raw: string): string {
  if (kind !== NotificationKind.EMAIL) return raw.trim();
  const list = raw.trim().replace(/^mailto:/, "").split(",").map((a) => a.trim());
  return "mailto:" + list.join(",");
}

// The end states a channel can post, as `bohux channel create --state` names them.
const endStates = [DeploymentState.SUCCEEDED, DeploymentState.FAILED, DeploymentState.ROLLED_BACK, DeploymentState.CANCELLED];

// Channels lists a project's deploy notification channels, creates, and removes them
// (ADR-0038).
export function Channels({ projectId }: { projectId: string }) {
  const list = useQuery(NotificationService.method.listNotificationChannels, { projectId, pageSize: 100 });
  const create = useMutation(NotificationService.method.createNotificationChannel);
  const remove = useMutation(NotificationService.method.deleteNotificationChannel);
  const qc = useQueryClient();
  const [kind, setKind] = useState(NotificationKind.SLACK);
  const [url, setURL] = useState("");
  const [states, setStates] = useState<DeploymentState[]>([]);
  const [secret, setSecret] = useState<string>();
  const [error, setError] = useState<string>();
  const stateName = (s: DeploymentState) => enumName("DEPLOYMENT_STATE_", DeploymentState[s]);
  return (
    <div>
      <h2 className="mb-2 font-semibold">Deploy notifications</h2>
      <Table
        head={["Channel", "Kind", "Posts to", "States", ""]}
        empty="No channels. Deploys that end post nowhere."
        rows={(list.data?.channels ?? []).map((c) => [
          short(c.meta?.id),
          enumName("NOTIFICATION_KIND_", NotificationKind[c.kind]),
          c.urlHost,
          c.states.length ? c.states.map(stateName).join(", ") : "all end states",
          <button className="text-danger" onClick={async () => {
            if (!window.confirm(`Remove the ${c.urlHost} channel?`)) return;
            setError(undefined);
            try {
              await remove.mutateAsync({ id: c.meta?.id, expectedRevisionId: c.meta?.revisionId, idempotencyKey: key() });
              await qc.invalidateQueries();
            } catch (err) {
              setError(errorText(err));
            }
          }}>remove</button>,
        ])}
      />
      {secret && <div className="mt-3"><Once label="The channel's signing secret (verify posts with it)" value={secret}
        onDone={() => setSecret(undefined)} /></div>}
      <div className="mt-3">
        <Toggle label="New channel">
          {(close) => (
            <form className="flex flex-wrap items-end gap-3" onSubmit={async (e) => {
              e.preventDefault();
              setError(undefined);
              try {
                const r = await create.mutateAsync({ projectId, kind, url: target(kind, url), states, idempotencyKey: key() });
                setURL("");
                if (r.signingSecret) setSecret(r.signingSecret);
                await qc.invalidateQueries();
                close();
              } catch (err) {
                setError(errorText(err));
              }
            }}>
              <Field id="chan-kind" label="Kind">
                <select id="chan-kind" className={input} value={kind} onChange={(e) => setKind(Number(e.target.value))}>
                  <option value={NotificationKind.SLACK}>Slack</option>
                  <option value={NotificationKind.DISCORD}>Discord</option>
                  <option value={NotificationKind.WEBHOOK}>Webhook</option>
                  <option value={NotificationKind.EMAIL}>Email</option>
                </select>
              </Field>
              <Field id="chan-url" label={kind === NotificationKind.EMAIL
                ? "Recipients, comma-separated (kept secret)" : "URL (kept secret, never shown again)"}>
                <input id="chan-url" type={kind === NotificationKind.EMAIL ? "text" : "password"} autoComplete="off" className={input} value={url}
                  onChange={(e) => setURL(e.target.value)} />
              </Field>
              <fieldset className="flex gap-3 text-sm">
                <legend className="text-muted">States (none: all)</legend>
                {endStates.map((s) => (
                  <label key={s} className="flex items-center gap-1">
                    <input type="checkbox" checked={states.includes(s)}
                      onChange={(e) => setStates(e.target.checked ? [...states, s] : states.filter((x) => x !== s))} />
                    {stateName(s)}
                  </label>
                ))}
              </fieldset>
              <button type="submit" disabled={!url.trim() || create.isPending} className="text-accent disabled:text-muted">Create</button>
              <button type="button" className="text-muted" onClick={close}>Cancel</button>
            </form>
          )}
        </Toggle>
      </div>
      {error && <p className="mt-2 text-sm text-danger" role="alert">{error}</p>}
    </div>
  );
}
