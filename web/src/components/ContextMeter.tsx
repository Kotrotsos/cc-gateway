import { useEffect, useState } from "react";
import { Layers } from "lucide-react";
import { api, type SessionDetail } from "@/lib/api";
import { cn, contextWindow, fmtTokens, shortModel } from "@/lib/utils";
import { groupThreads, type Thread } from "@/lib/threads";

// ContextMeter shows how full the model's context window is, refreshing on every
// live tick so it visibly fills as a run proceeds. A Claude Code session can
// interleave the main thread with subagent threads (same session id, separate
// conversations); each gets its own bar. Within a thread the context grows
// monotonically, so the peak prompt is its high-water mark.
export function ContextMeter({ sessionId, version }: { sessionId: number; version: number }) {
  const [detail, setDetail] = useState<SessionDetail | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.session(sessionId).then((d) => !cancelled && setDetail(d));
    return () => {
      cancelled = true;
    };
  }, [sessionId, version]);

  if (!detail || detail.requests.length === 0) return null;
  const threads = groupThreads(detail.requests);
  const subs = threads.length - 1;

  return (
    <div className="shrink-0 border-b bg-muted/30 px-4 py-2">
      <div className="mb-1.5 flex items-center gap-2 text-[11px] text-muted-foreground">
        <Layers className="h-3.5 w-3.5" />
        <span className="font-semibold text-foreground">Context window</span>
        {subs > 0 && (
          <span>
            main + {subs} subagent{subs > 1 ? "s" : ""}
          </span>
        )}
        <span className="ml-auto flex items-center gap-3">
          <Swatch className="bg-sky-500/50" label="cache read" />
          <Swatch className="bg-amber-500/60" label="cache write" />
          <Swatch className="bg-primary" label="input" />
        </span>
      </div>

      <div className="space-y-1">
        {threads.map((t) => (
          <ThreadBar key={t.key} t={t} solo={threads.length === 1} />
        ))}
      </div>
    </div>
  );
}

function ThreadBar({ t, solo }: { t: Thread; solo: boolean }) {
  const model = t.peak.model;
  const win = contextWindow(model);
  const pct = win > 0 ? Math.min(100, (t.peakTotal / win) * 100) : 0;
  const seg = (n: number) => (win > 0 ? (n / win) * 100 : 0);
  const cached = seg(t.peak.cache_read);
  const written = seg(t.peak.cache_write);
  const fresh = seg(t.peak.in_tokens);
  const tone = pct >= 95 ? "text-destructive" : pct >= 80 ? "text-amber-600 dark:text-amber-400" : "text-foreground/80";

  return (
    <div className="flex items-center gap-2">
      {!solo && (
        <span
          className={cn("w-20 shrink-0 truncate text-[10px] font-medium", t.isMain ? "text-foreground" : "text-muted-foreground")}
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
      <span className="w-16 shrink-0 truncate text-right text-[10px] text-muted-foreground" title={model}>
        {shortModel(model)}
      </span>
      <span className={cn("w-32 shrink-0 text-right text-[11px] font-medium tabular-nums", tone)}>
        {fmtTokens(t.peakTotal)} / {fmtTokens(win)} · {pct.toFixed(0)}%
      </span>
    </div>
  );
}

function Swatch({ className, label }: { className: string; label: string }) {
  return (
    <span className="flex items-center gap-1 text-[10px]">
      <span className={cn("h-2 w-2 rounded-sm", className)} />
      {label}
    </span>
  );
}
