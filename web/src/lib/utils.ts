import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "k";
  return String(n);
}

export function fmtCost(n: number): string {
  if (!n) return "$0.00";
  if (n < 0.01) return "<$0.01";
  return "$" + n.toFixed(2);
}

export function fmtDuration(ms: number): string {
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60_000) return (ms / 1000).toFixed(1) + "s";
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

export function fmtTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function fmtDateTime(ms: number): string {
  return new Date(ms).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function fmtRelative(ms: number): string {
  const diff = Date.now() - ms;
  const s = Math.floor(diff / 1000);
  if (s < 60) return s + "s ago";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m ago";
  const h = Math.floor(m / 60);
  if (h < 24) return h + "h ago";
  return Math.floor(h / 24) + "d ago";
}

// shortModel trims the long model id to its memorable part, e.g.
// "claude-opus-4-8-20260101" -> "opus-4-8".
export function shortModel(model: string): string {
  if (!model) return "";
  return model.replace(/^claude-/, "").replace(/-\d{8}$/, "").replace(/\[.*\]$/, "");
}

// firstLines returns the first n non-empty lines of a string, joined.
export function firstLines(text: string, n: number): { head: string; hasMore: boolean } {
  const lines = text.replace(/\r/g, "").split("\n");
  const head = lines.slice(0, n).join("\n");
  return { head, hasMore: lines.length > n || text.length > head.length };
}
