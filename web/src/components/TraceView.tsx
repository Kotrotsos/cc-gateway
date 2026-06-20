import { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, ChevronRight, Folder, GitBranch, Layers, Terminal } from "lucide-react";
import { api, type RequestSummary, type SessionDetail, type Span } from "@/lib/api";
import { cn, fmtCost, fmtDateTime, fmtDuration, fmtTime, fmtTokens, shortModel } from "@/lib/utils";
import { continuationRuns, groupThreads } from "@/lib/threads";
import { Badge } from "./ui/primitives";

// TraceView is the middle panel: a selectable list of the session's requests
// (#1, #2, …) with the tool spans that ran after each. The selected request's
// full message exchange is rendered by the parent in a separate detail panel.
// Requests can optionally be grouped by conversation thread (main + subagents).
export function TraceView({
  sessionId,
  version,
  selectedRequest,
  onSelectRequest,
  focusRequest,
  onFilterTool,
  groupByThread,
  onToggleGroup,
}: {
  sessionId: number;
  version: number;
  selectedRequest?: number;
  onSelectRequest: (id: number, baselineMessages: number) => void;
  focusRequest?: number;
  onFilterTool: (name: string) => void;
  groupByThread: boolean;
  onToggleGroup: () => void;
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

  const threads = useMemo(() => groupThreads(detail?.requests ?? []), [detail]);

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
          {threads.length > 1 && (
            <button
              onClick={onToggleGroup}
              className={cn(
                "ml-auto flex items-center gap-1 rounded-md border px-2 py-1 text-[11px] font-medium transition-colors",
                groupByThread ? "border-primary/40 bg-accent text-foreground" : "text-muted-foreground hover:bg-muted/60",
              )}
              title="Group requests by conversation thread (main + subagents)"
            >
              <Layers className="h-3 w-3" />
              Group by thread
            </button>
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
        {groupByThread && threads.length > 1 ? (
          <div className="space-y-4">
            {threads.map((t) => (
              <div key={t.key}>
                <div className="mb-1.5 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide">
                  <Layers className={cn("h-3 w-3", t.isMain ? "text-primary" : "text-muted-foreground")} />
                  <span className={t.isMain ? "text-foreground" : "text-muted-foreground"}>{t.label}</span>
                  <span className="font-normal normal-case text-muted-foreground">
                    {t.requests.length} req · {shortModel(t.peak.model)}
                  </span>
                </div>
                <RunList
                  requests={t.requests}
                  spansByCall={spansByCall}
                  selectedRequest={selectedRequest}
                  focusRequest={focusRequest}
                  onSelectRequest={onSelectRequest}
                  onFilterTool={onFilterTool}
                />
              </div>
            ))}
          </div>
        ) : (
          <RunList
            requests={detail.requests}
            spansByCall={spansByCall}
            selectedRequest={selectedRequest}
            focusRequest={focusRequest}
            onSelectRequest={onSelectRequest}
            onFilterTool={onFilterTool}
          />
        )}
      </div>
    </div>
  );
}

// RunList groups requests into continuation runs (see continuationRuns) and
// renders each as a group: the first request carries the full context; the rest
// are continuations indented under a green branch line, collapsible.
function RunList({
  requests,
  spansByCall,
  selectedRequest,
  focusRequest,
  onSelectRequest,
  onFilterTool,
}: {
  requests: RequestSummary[];
  spansByCall: Map<number, Span[]>;
  selectedRequest?: number;
  focusRequest?: number;
  onSelectRequest: (id: number, baselineMessages: number) => void;
  onFilterTool: (name: string) => void;
}) {
  const runs = useMemo(() => continuationRuns(requests), [requests]);
  return (
    <ol className="relative">
      {runs.map((run, ri) => (
        <RunGroup
          key={run[0].id}
          run={run}
          isLastRun={ri === runs.length - 1}
          spansByCall={spansByCall}
          selectedRequest={selectedRequest}
          focusRequest={focusRequest}
          onSelectRequest={onSelectRequest}
          onFilterTool={onFilterTool}
        />
      ))}
    </ol>
  );
}

function RunGroup({
  run,
  isLastRun,
  spansByCall,
  selectedRequest,
  focusRequest,
  onSelectRequest,
  onFilterTool,
}: {
  run: RequestSummary[];
  isLastRun: boolean;
  spansByCall: Map<number, Span[]>;
  selectedRequest?: number;
  focusRequest?: number;
  onSelectRequest: (id: number, baselineMessages: number) => void;
  onFilterTool: (name: string) => void;
}) {
  const [collapsed, setCollapsed] = useState(false);
  const root = run[0];
  const children = run.slice(1);
  const hasChildren = children.length > 0;
  const err = root.status >= 400 || !!root.error;

  return (
    <li className="relative pb-3 pl-7">
      {/* main timeline rail connecting run roots */}
      {!isLastRun && <span className="absolute left-[9px] top-5 h-full w-px bg-border" />}
      <span
        className={cn(
          "absolute left-1 top-[7px] h-3.5 w-3.5 rounded-full border-2 bg-background",
          err ? "border-destructive" : "border-primary",
        )}
      />

      <RequestNode
        req={root}
        spans={spansByCall.get(root.id) ?? []}
        selected={root.id === selectedRequest}
        focus={root.id === focusRequest}
        onSelect={() => onSelectRequest(root.id, 0)}
        onFilterTool={onFilterTool}
        collapsible={hasChildren}
        collapsed={collapsed}
        onToggleCollapse={() => setCollapsed((c) => !c)}
        childCount={children.length}
      />

      {hasChildren && !collapsed && (
        <div className="ml-2 mt-2 space-y-2 border-l-2 border-emerald-500/50 pl-4">
          {children.map((child, i) => (
            <RequestNode
              key={child.id}
              req={child}
              indented
              delta={child.num_messages - run[i].num_messages}
              spans={spansByCall.get(child.id) ?? []}
              selected={child.id === selectedRequest}
              focus={child.id === focusRequest}
              onSelect={() => onSelectRequest(child.id, run[i].num_messages)}
              onFilterTool={onFilterTool}
            />
          ))}
        </div>
      )}
    </li>
  );
}

function RequestNode({
  req,
  spans,
  selected,
  focus,
  onSelect,
  onFilterTool,
  indented,
  delta,
  collapsible,
  collapsed,
  onToggleCollapse,
  childCount,
}: {
  req: RequestSummary;
  spans: Span[];
  selected: boolean;
  focus: boolean;
  onSelect: () => void;
  onFilterTool: (name: string) => void;
  indented?: boolean;
  delta?: number;
  collapsible?: boolean;
  collapsed?: boolean;
  onToggleCollapse?: () => void;
  childCount?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (focus) ref.current?.scrollIntoView({ behavior: "smooth", block: "center" });
  }, [focus]);

  const err = req.status >= 400 || !!req.error;
  const maxSpan = Math.max(1, ...spans.map((s) => s.duration_ms || 0));

  return (
    <div ref={ref} className="relative">
      {/* green node marker for indented continuations, sitting on the branch line */}
      {indented && <span className="absolute -left-[19px] top-[11px] h-2 w-2 rounded-full bg-emerald-500/70" />}

      <div
        onClick={onSelect}
        role="button"
        tabIndex={0}
        className={cn(
          "flex w-full cursor-pointer items-center gap-2 rounded-lg border bg-card px-3 py-2 text-left transition-colors",
          selected ? "border-primary/40 bg-accent ring-1 ring-primary/30" : "hover:bg-muted/60",
          focus && !selected && "ring-1 ring-ring",
        )}
      >
        {collapsible && (
          <button
            onClick={(e) => {
              e.stopPropagation();
              onToggleCollapse?.();
            }}
            className="-ml-1 shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted"
            title={collapsed ? `Expand ${childCount} continuation${childCount === 1 ? "" : "s"}` : "Collapse continuations"}
          >
            <ChevronRight className={cn("h-3.5 w-3.5 transition-transform", !collapsed && "rotate-90")} />
          </button>
        )}
        <span className="shrink-0 text-xs font-semibold text-muted-foreground">#{req.seq}</span>
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{fmtTime(req.ts_start)}</span>
        {err ? <Badge variant="error">{req.status || "error"}</Badge> : <Badge variant="success">{req.status}</Badge>}
        {indented && typeof delta === "number" && (
          <span
            className="shrink-0 rounded bg-emerald-500/10 px-1.5 py-px text-[10px] font-medium text-emerald-700 dark:text-emerald-400"
            title="new messages added by this request"
          >
            +{delta} msg
          </span>
        )}
        <span className="min-w-0 flex-1 truncate text-sm text-foreground/80">
          {req.assistant_preview || <span className="text-muted-foreground">{req.stop_reason || "—"}</span>}
        </span>
        {collapsed && childCount ? (
          <span className="shrink-0 text-[11px] font-medium text-emerald-700 dark:text-emerald-400">+{childCount} more</span>
        ) : null}
        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
          {fmtDuration(req.duration_ms)} · ↑{fmtTokens(req.in_tokens)} ↓{fmtTokens(req.out_tokens)}
        </span>
      </div>

      <NodeSpans spans={spans} max={maxSpan} onFilterTool={onFilterTool} />
    </div>
  );
}

// NodeSpans renders the reconstructed tool spans that ran after a request.
function NodeSpans({ spans, max, onFilterTool }: { spans: Span[]; max: number; onFilterTool: (n: string) => void }) {
  if (spans.length === 0) return null;
  return (
    <div className="ml-3 mt-1.5 space-y-1">
      {spans.map((sp) => (
        <SpanBar key={sp.tool_use_id} sp={sp} max={max} onFilterTool={onFilterTool} />
      ))}
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
