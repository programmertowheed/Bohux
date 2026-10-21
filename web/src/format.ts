import type { Timestamp } from "@bufbuild/protobuf/wkt";

// enumName turns DEPLOYMENT_STATE_ROLLED_BACK into "rolled back".
export function enumName(prefix: string, name: string | undefined): string {
  if (!name) return "unknown";
  return name.replace(prefix, "").toLowerCase().replace(/_/g, " ");
}

export function when(ts: Timestamp | undefined): string {
  if (!ts) return "";
  const d = new Date(Number(ts.seconds) * 1000);
  return d.toLocaleString();
}

export function short(id: string | undefined): string {
  return id ? id.slice(-8) : "";
}
