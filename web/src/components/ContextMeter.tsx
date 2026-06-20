import { useEffect, useState } from "react";
import { Layers } from "lucide-react";
import { api, type SessionDetail } from "@/lib/api";
import { cn, contextWindow, fmtTokens, shortModel } from "@/lib/utils";

// ContextMeter shows how full the model's context window is for the selected
// session, refreshing on every live tick so it visibly fills as a run proceeds.
//
// A Claude Code session can interleave the main thread with subagent threads,
// which share the same session id. The main thread's context grows
// monotonically while subagent bursts are small and independent, so the peak
// total-prompt across requests tracks the main window's high-water mark and
// ignores subagent dips.
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

  let peakTotal = 0;
  let peak = detail.requests[0];
  for (const r of detail.requests) {
    const t = r.in_tokens + r.cache_read + r.cache_write;
    if (t >= peakTotal) {
      peakTotal = t;
      peak = r;
    }
  }

  const model = peak.model || detail.session.model;
  const win = contextWindow(model);
  const pct = win > 0 ? Math.min(100, (peakTotal / win) * 100) : 0;
  const seg = (n: number) => (win > 0 ? (n / win) * 100 : 0);
  const cached = seg(peak.cache_read);
  const written = seg(peak.cache_write);
  const fresh = seg(peak.in_tokens);

  const tone = pct >= 95 ? "text-destructive" : pct >= 80 ? "text-amber-600 dark:text-amber-400" : "text-foreground/80";

  return (
    <div className="shrink-0 border-b bg-muted/30 px-4 py-2">
      <div className="mb-1.5 flex items-center gap-2 text-[11px]">
        <Layers className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="font-semibold">Context window</span>
        <span className="text-muted-foreground">
          {shortModel(model)} · {fmtTokens(win)} max
        </span>
        <span className={cn("ml-auto font-semibold tabular-nums", tone)}>
          {fmtTokens(peakTotal)} / {fmtTokens(win)} · {pct.toFixed(0)}%
        </span>
      </div>

      <div className="relative h-2.5 w-full overflow-hidden rounded-full bg-muted">
        <div className="absolute inset-y-0 bg-sky-500/50" style={{ left: 0, width: `${cached}%` }} />
        <div className="absolute inset-y-0 bg-amber-500/60" style={{ left: `${cached}%`, width: `${written}%` }} />
        <div className="absolute inset-y-0 bg-primary" style={{ left: `${cached + written}%`, width: `${fresh}%` }} />
      </div>

      <div className="mt-1 flex items-center gap-3 text-[10px] text-muted-foreground">
        <Legend className="bg-sky-500/50" label={`cache read ${fmtTokens(peak.cache_read)}`} />
        <Legend className="bg-amber-500/60" label={`cache write ${fmtTokens(peak.cache_write)}`} />
        <Legend className="bg-primary" label={`input ${fmtTokens(peak.in_tokens)}`} />
        <span className="ml-auto tabular-nums">peak of {detail.requests.length} requests</span>
      </div>
    </div>
  );
}

function Legend({ className, label }: { className: string; label: string }) {
  return (
    <span className="flex items-center gap-1">
      <span className={cn("h-2 w-2 rounded-sm", className)} />
      {label}
    </span>
  );
}
