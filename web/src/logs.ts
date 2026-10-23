// stripANSI removes terminal escape sequences, so log lines render as plain text.
// The log view draws no ANSI colors (O27); the terminal page uses xterm.js (ADR-0046).
// eslint-disable-next-line no-control-regex
const ansi = /\u001b\[[0-9;?]*[ -/]*[@-~]|\u001b\][^\u0007]*\u0007/g;

export function stripANSI(s: string): string {
  return s.replace(ansi, "");
}

// keepLast bounds the lines kept in memory while following.
export function keepLast<T>(lines: T[], max: number): T[] {
  return lines.length > max ? lines.slice(lines.length - max) : lines;
}
