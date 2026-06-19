import { useEffect, useMemo, useState } from "react";
import { Search, X } from "lucide-react";
import { api, type SessionSummary } from "@/lib/api";
import type { LiveState } from "@/lib/live";
import type { Focus } from "@/App";
import { Input, Select } from "./ui/primitives";
import { cn } from "@/lib/utils";
import { SessionRow } from "./SessionList";
import { TraceView } from "./TraceView";

export function Explorer({ live, focus, clearFocus }: { live: LiveState; focus: Focus; clearFocus: () => void }) {
  const [sessions, setSessions] = useState<SessionSummary[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [focusReq, setFocusReq] = useState<number | undefined>(undefined);

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
      setFocusReq(focus.requestId);
      clearFocus();
    }
  }, [focus]);

  return (
    <div className="flex h-full">
      <aside className="flex w-[340px] shrink-0 flex-col border-r">
        <div className="flex flex-col gap-2 border-b p-2.5">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search message text…"
              className="pl-7"
            />
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
              <SessionRow key={s.id} s={s} active={s.id === selected} onClick={() => setSelected(s.id)} />
            ))
          )}
        </div>
      </aside>

      <section className="min-h-0 flex-1 overflow-hidden">
        {selected != null ? (
          <TraceView
            sessionId={selected}
            version={live.version}
            focusRequest={focusReq}
            onFocusConsumed={() => setFocusReq(undefined)}
            onFilterTool={setTool}
          />
        ) : (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            Select a session to view its trace.
          </div>
        )}
      </section>
    </div>
  );
}
