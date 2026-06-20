import { useEffect, useState } from "react";
import { ChevronDown, Layers } from "lucide-react";
import { api, type SessionDetail } from "@/lib/api";
import { cn, contextWindowFor, fmtTokens, shortModel } from "@/lib/utils";
import { groupThreads, type Thread } from "@/lib/threads";

const COLLAPSE_KEY = "cc-ctx-collapsed";

// ContextMeter shows how full the model's context window is, refreshing on every
// live tick so it visibly fills as a run proceeds. A Claude Code session can
// interleave the main thread with subagent threads (same session id, separate
// conversations); each gets its own bar. Within a thread the context grows
// monotonically, so the peak prompt is its high-water mark. The panel collapses
// to a single summary line, remembered across reloads.
export function ContextMeter({ sessionId, version }: { sessionId: number; version: number }) {
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [collapsed, setCollapsed] = useState(() => localStorage.getItem(COLLAPSE_KEY) === "1");

  useEffect(() => {
    let cancelled = false;
    api.session(sessionId).then((d) => !cancelled && setDetail(d));
    return () => {
      cancelled = true;
    };
  }, [sessionId, version]);

  function toggle() {
    setCollapsed((c) => {
      const next = !c;
      localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });
  }

  if (!detail || detail.requests.length === 0) return null;
  const threads = groupThreads(detail.requests);
  const subs = threads.length - 1;
  const main = threads[0];
  const mainPct = fillPct(main);

  return (
    <div className="shrink-0 border-b bg-muted/30 px-4 py-2">
      <div className="flex items-center gap-2 text-[11px]">
        <button onClick={toggle} className="flex items-center gap-1.5 text-foreground hover:text-foreground/80" title={collapsed ? "Expand" : "Collapse"}>
          <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", collapsed && "-rotate-90")} />
          <Layers className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="font-semibold">Context window</span>
        </button>
        {subs > 0 && (
          <span className="text-muted-foreground">
            main + {subs} subagent{subs > 1 ? "s" : ""}
          </span>
        )}
        <div className="ml-auto flex items-center gap-3">
          {collapsed ? (
            <span className="tabular-nums text-muted-foreground">
              main {fmtTokens(main.peakTotal)} · {mainPct.toFixed(0)}%
            </span>
          ) : (
            <>
              <Swatch className="bg-sky-500/50" label="cache read" />
              <Swatch className="bg-amber-500/60" label="cache write" />
              <Swatch className="bg-primary" label="input" />
            </>
          )}
        </div>
      </div>

      {!collapsed && (
        <div className="mt-1.5 space-y-1">
          {threads.map((t) => (
            <ThreadBar key={t.key} t={t} solo={threads.length === 1} />
          ))}
        </div>
      )}
    </div>
  );
}

function fillPct(t: Thread): number {
  const win = contextWindowFor(t.peak.model, t.peakTotal);
  return win > 0 ? Math.min(100, (t.peakTotal / win) * 100) : 0;
}

function ThreadBar({ t, solo }: { t: Thread; solo: boolean }) {
  const model = t.peak.model;
  const win = contextWindowFor(model, t.peakTotal);
  const pct = win > 0 ? Math.min(100, (t.peakTotal / win) * 100) : 0;
  const seg = (n: number) => (win > 0 ? (n / win) * 100 : 0);
  const cached = seg(t.peak.cache_read);
  const written = seg(t.peak.cache_write);
  const fresh = seg(t.peak.in_tokens);
  const tone = pct >= 95 ? "text-destructive" : pct >= 80 ? "text-amber-600 dark:text-amber-400" : "text-foreground/80";

  return (
    <div className="flex items-center gap-3">
      {!solo && (
        <span
          className={cn("w-20 shrink-0 truncate text-[11px] font-medium", t.isMain ? "text-foreground" : "text-muted-foreground")}
          title={`${t.label} · ${t.requests.length} requests`}
        >
          {t.label}
        </span>
      )}
      <div className="relative h-2.5 flex-1 overflow-hidden rounded-full bg-muted">
        <div className="absolute inset-y-0 bg-sky-500/50" style={{ left: 0, width: `${cached}%` }} />
        <div className="absolute inset-y-0 bg-amber-500/60" style={{ left: `${cached}%`, width: `${written}%` }} />
        <div className="absolute inset-y-0 bg-primary" style={{ left: `${cached + written}%`, width: `${fresh}%` }} />
      </div>
      <span className="w-16 shrink-0 truncate text-right text-[11px] text-muted-foreground" title={model}>
        {shortModel(model)}
      </span>
      <span className={cn("w-36 shrink-0 whitespace-nowrap text-right text-[11px] font-medium tabular-nums", tone)}>
        {fmtTokens(t.peakTotal)} / {fmtTokens(win)} · {pct.toFixed(0)}%
      </span>
    </div>
  );
}

function Swatch({ className, label }: { className: string; label: string }) {
  return (
    <span className="flex items-center gap-1 text-[10px] text-muted-foreground">
      <span className={cn("h-2 w-2 rounded-sm", className)} />
      {label}
    </span>
  );
}
