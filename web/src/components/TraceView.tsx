import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, Folder, GitBranch, Terminal } from "lucide-react";
import { api, type RequestSummary, type SessionDetail, type Span } from "@/lib/api";
import { cn, fmtCost, fmtDateTime, fmtDuration, fmtTime, fmtTokens, shortModel } from "@/lib/utils";
import { Badge } from "./ui/primitives";

// TraceView is the middle panel: a selectable list of the session's requests
// (#1, #2, …) with the tool spans that ran after each. The selected request's
// full message exchange is rendered by the parent in a separate detail panel.
export function TraceView({
  sessionId,
  version,
  selectedRequest,
  onSelectRequest,
  focusRequest,
  onFilterTool,
}: {
  sessionId: number;
  version: number;
  selectedRequest?: number;
  onSelectRequest: (id: number) => void;
  focusRequest?: number;
  onFilterTool: (name: string) => void;
}) {
  const [detail, setDetail] = useState<SessionDetail | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.session(sessionId).then((d) => !cancelled && setDetail(d));
    return () => {
      cancelled = true;
    };
  }, [sessionId, version]);

  const spansByCall = useMemo(() => {
    const m = new Map<number, Span[]>();
    detail?.spans.forEach((sp) => {
      const arr = m.get(sp.call_request_id) ?? [];
      arr.push(sp);
      m.set(sp.call_request_id, arr);
    });
    return m;
  }, [detail]);

  if (!detail) {
    return <div className="p-6 text-sm text-muted-foreground">Loading trace…</div>;
  }

  const s = detail.session;

  return (
    <div className="flex h-full flex-col">
      <header className="shrink-0 border-b px-5 py-3">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold">{s.cwd ? s.cwd.split("/").filter(Boolean).pop() : "session"}</h2>
          {s.model && <Badge variant="accent">{shortModel(s.model)}</Badge>}
          {s.error_count > 0 && (
            <Badge variant="error">
              <AlertTriangle className="h-3 w-3" /> {s.error_count}
            </Badge>
          )}
        </div>
        <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          {s.cwd && (
            <span className="flex items-center gap-1">
              <Folder className="h-3 w-3" />
              {s.cwd}
            </span>
          )}
          {s.git_branch && (
            <span className="flex items-center gap-1">
              <GitBranch className="h-3 w-3" />
              {s.git_branch}
            </span>
          )}
          {s.cli_version && (
            <span className="flex items-center gap-1">
              <Terminal className="h-3 w-3" />
              cli {s.cli_version}
            </span>
          )}
          <span>{fmtDateTime(s.first_seen)}</span>
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs tabular-nums">
          <Stat label="requests" value={String(s.num_requests)} />
          <Stat label="in" value={fmtTokens(s.in_tokens)} />
          <Stat label="out" value={fmtTokens(s.out_tokens)} />
          <Stat label="cache read" value={fmtTokens(s.cache_read)} />
          <Stat label="tools" value={String(detail.spans.length)} />
          <Stat label="est cost" value={fmtCost(s.est_cost)} />
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        <ol className="relative">
          {detail.requests.map((r, i) => (
            <RequestNode
              key={r.id}
              req={r}
              isLast={i === detail.requests.length - 1}
              spans={spansByCall.get(r.id) ?? []}
              selected={r.id === selectedRequest}
              focus={r.id === focusRequest}
              onSelect={() => onSelectRequest(r.id)}
              onFilterTool={onFilterTool}
            />
          ))}
        </ol>
      </div>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex items-baseline gap-1 rounded-md bg-muted px-2 py-1">
      <span className="font-semibold text-foreground">{value}</span>
      <span className="text-muted-foreground">{label}</span>
    </span>
  );
}

function RequestNode({
  req,
  isLast,
  spans,
  selected,
  focus,
  onSelect,
  onFilterTool,
}: {
  req: RequestSummary;
  isLast: boolean;
  spans: Span[];
  selected: boolean;
  focus: boolean;
  onSelect: () => void;
  onFilterTool: (name: string) => void;
}) {
  const ref = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (focus) ref.current?.scrollIntoView({ behavior: "smooth", block: "center" });
  }, [focus]);

  const err = req.status >= 400 || req.error;
  const maxSpan = Math.max(1, ...spans.map((s) => s.duration_ms || 0));

  return (
    <li ref={ref} className="relative pb-3 pl-7">
      {!isLast && <span className="absolute left-[9px] top-5 h-full w-px bg-border" />}
      <span
        className={cn(
          "absolute left-1 top-[7px] h-3.5 w-3.5 rounded-full border-2 bg-background",
          err ? "border-destructive" : "border-primary",
        )}
      />

      <button
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-2 rounded-lg border bg-card px-3 py-2 text-left transition-colors",
          selected ? "border-primary/40 bg-accent ring-1 ring-primary/30" : "hover:bg-muted/60",
          focus && !selected && "ring-1 ring-ring",
        )}
      >
        <span className="shrink-0 text-xs font-semibold text-muted-foreground">#{req.seq}</span>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{fmtTime(req.ts_start)}</span>
        {err ? (
          <Badge variant="error">{req.status || "error"}</Badge>
        ) : (
          <Badge variant="success">{req.status}</Badge>
        )}
        <span className="min-w-0 flex-1 truncate text-sm text-foreground/80">
          {req.assistant_preview || <span className="text-muted-foreground">{req.stop_reason || "—"}</span>}
        </span>
        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
          {fmtDuration(req.duration_ms)} · ↑{fmtTokens(req.in_tokens)} ↓{fmtTokens(req.out_tokens)}
        </span>
      </button>

      {/* Reconstructed tool spans that ran after this request. */}
      {spans.length > 0 && (
        <div className="ml-3 mt-1.5 space-y-1">
          {spans.map((sp) => (
            <SpanBar key={sp.tool_use_id} sp={sp} max={maxSpan} onFilterTool={onFilterTool} />
          ))}
        </div>
      )}
    </li>
  );
}

function SpanBar({ sp, max, onFilterTool }: { sp: Span; max: number; onFilterTool: (n: string) => void }) {
  const pct = sp.has_result ? Math.max(4, ((sp.duration_ms || 0) / max) * 100) : 0;
  return (
    <div className="flex items-center gap-2 text-xs">
      <button
        onClick={() => onFilterTool(sp.name)}
        className="w-28 shrink-0 truncate text-left font-medium hover:underline"
        title={sp.input_preview}
      >
        {sp.name}
      </button>
      <div className="relative h-3.5 flex-1 overflow-hidden rounded bg-muted">
        <div
          className={cn("absolute inset-y-0 left-0 rounded", sp.is_error ? "bg-destructive/60" : "bg-primary/40")}
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="w-24 shrink-0 text-right tabular-nums text-muted-foreground">
        {sp.has_result ? fmtDuration(sp.duration_ms) : "pending"}
        {sp.is_error && <span className="text-destructive"> err</span>}
      </span>
    </div>
  );
}
