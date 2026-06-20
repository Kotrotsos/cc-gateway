import { useEffect, useMemo, useState } from "react";
import { Panel, PanelGroup, PanelResizeHandle } from "react-resizable-panels";
import { ChevronsDownUp, Search, X } from "lucide-react";
import { api, type SessionSummary } from "@/lib/api";
import type { LiveState } from "@/lib/live";
import type { Focus } from "@/App";
import { Input, Select } from "./ui/primitives";
import { cn } from "@/lib/utils";
import { SessionRow } from "./SessionList";
import { TraceView } from "./TraceView";
import { RequestBody } from "./RequestBody";
import { ContextMeter } from "./ContextMeter";

export function Explorer({ live, focus, clearFocus }: { live: LiveState; focus: Focus; clearFocus: () => void }) {
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [selectedReq, setSelectedReq] = useState<number | undefined>(undefined);
  const [selectedBaseline, setSelectedBaseline] = useState<number | undefined>(undefined);
  const [focusReq, setFocusReq] = useState<number | undefined>(undefined);
  const [collapseNonce, setCollapseNonce] = useState(0);
  const [groupByThread, setGroupByThread] = useState(false);

  const [model, setModel] = useState("");
  const [tool, setTool] = useState("");
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [q, setQ] = useState("");

  const params = useMemo(() => {
    const p: Record<string, string> = {};
    if (model) p.model = model;
    if (tool) p.tool = tool;
    if (errorsOnly) p.errors = "1";
    if (q.trim()) p.q = q.trim();
    return p;
  }, [model, tool, errorsOnly, q]);

  // Reload the list on filter changes and whenever a new exchange streams in.
  useEffect(() => {
    let cancelled = false;
    api.sessions(params).then((s) => {
      if (cancelled) return;
      setSessions(s);
      setSelected((cur) => (cur != null && s.some((x) => x.id === cur) ? cur : s[0]?.id ?? null));
    });
    return () => {
      cancelled = true;
    };
  }, [params, live.version]);

  useEffect(() => {
    api.models().then(setModels).catch(() => {});
  }, [live.version === 0]);

  // Respond to a jump-to-trace request from Analytics.
  useEffect(() => {
    if (focus) {
      setSelected(focus.sessionId);
      setSelectedReq(focus.requestId);
      setSelectedBaseline(undefined); // jumped-to request: show full context
      setFocusReq(focus.requestId);
      clearFocus();
    }
  }, [focus]);

  function pickSession(id: number) {
    setSelected(id);
    setSelectedReq(undefined);
    setSelectedBaseline(undefined);
  }

  return (
    <div className="flex h-full flex-col">
      {selected != null && <ContextMeter sessionId={selected} version={live.version} />}
      <PanelGroup direction="horizontal" autoSaveId="cc-explorer" className="min-h-0 flex-1">
        {/* Sessions / projects */}
      <Panel defaultSize={24} minSize={15} className="flex flex-col border-r">
        <div className="flex flex-col gap-2 border-b p-2.5">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search message text…" className="pl-7" />
          </div>
          <div className="flex items-center gap-1.5">
            <Select value={model} onChange={(e) => setModel(e.target.value)} className="flex-1">
              <option value="">All models</option>
              {models.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </Select>
            {tool && (
              <button
                onClick={() => setTool("")}
                className="flex items-center gap-1 rounded-md bg-accent px-2 py-1 text-xs"
                title="Clear tool filter"
              >
                {tool}
                <X className="h-3 w-3" />
              </button>
            )}
            <button
              onClick={() => setErrorsOnly((v) => !v)}
              className={cn(
                "rounded-md border px-2 py-1 text-xs font-medium",
                errorsOnly ? "border-destructive/40 text-destructive" : "text-muted-foreground",
              )}
            >
              Errors
            </button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {sessions.length === 0 ? (
            <div className="p-6 text-center text-sm text-muted-foreground">
              No sessions yet. Point Claude Code at the gateway and start a run.
            </div>
          ) : (
            sessions.map((s) => (
              <SessionRow key={s.id} s={s} active={s.id === selected} onClick={() => pickSession(s.id)} />
            ))
          )}
        </div>
      </Panel>

      <ResizeHandle />

      {/* Requests (#1, #2, …) */}
      <Panel defaultSize={38} minSize={22} className="min-h-0 overflow-hidden">
        {selected != null ? (
          <TraceView
            sessionId={selected}
            version={live.version}
            selectedRequest={selectedReq}
            onSelectRequest={(id, baseline) => {
              setSelectedReq(id);
              setSelectedBaseline(baseline);
              setFocusReq(undefined);
            }}
            focusRequest={focusReq}
            onFilterTool={setTool}
            groupByThread={groupByThread}
            onToggleGroup={() => setGroupByThread((v) => !v)}
          />
        ) : (
          <Placeholder>Select a session to view its requests.</Placeholder>
        )}
      </Panel>

      <ResizeHandle />

      {/* Messages for the selected request */}
      <Panel defaultSize={38} minSize={22} className="flex min-h-0 flex-col border-l">
        {selectedReq != null ? (
          <>
            <div className="flex h-9 shrink-0 items-center gap-2 border-b bg-muted/40 px-3 text-[11px] font-medium text-muted-foreground">
              <span>Messages</span>
              <button
                onClick={() => setCollapseNonce((n) => n + 1)}
                className="ml-auto flex items-center gap-1 rounded-md border px-2 py-1 hover:bg-accent"
                title="Collapse all expanded sections"
              >
                <ChevronsDownUp className="h-3 w-3" />
                Collapse all
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto">
              <RequestBody requestId={selectedReq} collapseNonce={collapseNonce} baselineMessages={selectedBaseline} />
            </div>
          </>
        ) : (
          <Placeholder>Select a request to view its messages.</Placeholder>
        )}
        </Panel>
      </PanelGroup>
    </div>
  );
}

function ResizeHandle() {
  return (
    <PanelResizeHandle className="group relative w-px bg-border transition-colors data-[resize-handle-state=drag]:bg-primary data-[resize-handle-state=hover]:bg-primary/60">
      <span className="absolute inset-y-0 -left-1 -right-1" />
    </PanelResizeHandle>
  );
}

function Placeholder({ children }: { children: React.ReactNode }) {
  return <div className="flex h-full items-center justify-center px-6 text-center text-sm text-muted-foreground">{children}</div>;
}
