import type { ReactNode } from "react";

// Table is a plain, accessible table: headers are <th scope="col">.
export function Table({ head, rows, empty }: { head: string[]; rows: ReactNode[][]; empty: string }) {
  if (rows.length === 0) return <p className="text-muted">{empty}</p>;
  return (
    <table className="w-full border-collapse text-sm">
      <thead>
        <tr className="border-b border-border text-left text-muted">
          {head.map((h) => (
            <th key={h} scope="col" className="py-2 pr-4 font-medium">{h}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i} className="border-b border-border">
            {r.map((c, j) => (
              <td key={j} className="py-2 pr-4 align-top">{c}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function Loading({ what }: { what: string }) {
  return <p className="text-muted" role="status">Loading {what}…</p>;
}

export function Failed({ error }: { error: { message: string } }) {
  return <p className="text-danger" role="alert">{error.message}</p>;
}
