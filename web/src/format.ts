import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { BuildStrategy } from "./gen/bohux/v1/app_pb";

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

// bytes prints a byte count as KiB, MiB, or GiB, like the CLI.
export function bytes(n: bigint | number): string {
  const v = Number(n);
  if (v >= 2 ** 30) return `${(v / 2 ** 30).toFixed(1)}GiB`;
  if (v >= 2 ** 20) return `${Math.round(v / 2 ** 20)}MiB`;
  if (v >= 2 ** 10) return `${Math.round(v / 2 ** 10)}KiB`;
  return `${v}B`;
}

// buildName says how a Git App builds (ADR-0047).
export function buildName(s: BuildStrategy): string {
  switch (s) {
    case BuildStrategy.DOCKERFILE:
      return "its Dockerfile";
    case BuildStrategy.RAILPACK:
      return "Railpack";
    default:
      return "its Dockerfile, or Railpack if it has none";
  }
}
