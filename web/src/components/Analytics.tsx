import { useEffect, useMemo, useState } from "react";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { ArrowRight, X } from "lucide-react";
import { api, type Analytics, type ToolCallRef } from "@/lib/api";
import { Card } from "./ui/primitives";
import { cn, fmtCost, fmtDuration, fmtTime, fmtTokens, shortModel } from "@/lib/utils";

const RANGES: { label: string; ms: number; bucket: number }[] = [
  { label: "1h", ms: 3600_000, bucket: 120_000 },
  { label: "24h", ms: 86_400_000, bucket: 3600_000 },
  { label: "7d", ms: 7 * 86_400_000, bucket: 6 * 3600_000 },
  { label: "All", ms: 0, bucket: 86_400_000 },
];

const PIE = ["#4f46e5", "#0891b2", "#16a34a", "#d97706", "#db2777", "#7c3aed", "#64748b"];

export function AnalyticsView({ onJump }: { onJump: (sessionId: number, requestId?: number) => void }) {
  const [data, setData] = useState<Analytics | null>(null);
  const [rangeIdx, setRangeIdx] = useState(3);
  const [tool, setTool] = useState<string | null>(null);

  const range = RANGES[rangeIdx];
  const params = useMemo(() => {
    const p: Record<string, string> = { bucket: String(range.bucket) };
    if (range.ms) p.since = String(Date.now() - range.ms);
    return p;
  }, [rangeIdx]);

  useEffect(() => {
    api.analytics(params).then(setData);
  }, [params]);

  if (!data) return <div className="p-6 text-sm text-muted-foreground">Loading analytics…</div>;

  const t = data.totals;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-6xl space-y-4 p-5">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-semibold">Analytics</h2>
          <div className="flex items-center gap-1 rounded-md border p-0.5">
            {RANGES.map((r, i) => (
              <button
                key={r.label}
                onClick={() => setRangeIdx(i)}
                className={cn(
                  "rounded px-2 py-0.5 text-xs font-medium transition-colors",
                  i === rangeIdx ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {r.label}
              </button>
            ))}
          </div>
        </div>

        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <StatCard label="Sessions" value={String(t.sessions)} />
          <StatCard label="Requests" value={String(t.requests)} />
          <StatCard label="Tool calls" value={String(t.tool_calls)} />
          <StatCard label="Est. cost" value={fmtCost(t.est_cost)} hint="approximate" />
          <StatCard label="Input tokens" value={fmtTokens(t.in_tokens)} />
          <StatCard label="Output tokens" value={fmtTokens(t.out_tokens)} />
          <StatCard label="Cache hit" value={Math.round(data.cache_hit_rate * 100) + "%"} />
          <StatCard label="Avg latency" value={fmtDuration(t.avg_latency_ms)} hint={t.errors ? `${t.errors} errors` : undefined} />
        </div>

        <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <Panel title="Requests over time">
            <ResponsiveContainer width="100%" height={180}>
              <AreaChart data={data.series}>
                <XAxis dataKey="bucket" tickFormatter={fmtTime} tick={axisTick} stroke="currentColor" className="text-muted-foreground" />
                <YAxis tick={axisTick} width={28} stroke="currentColor" className="text-muted-foreground" />
                <Tooltip content={<ChartTip labelFmt={fmtTime} />} />
                <Area type="monotone" dataKey="requests" stroke="#4f46e5" fill="#4f46e5" fillOpacity={0.15} strokeWidth={1.5} />
              </AreaChart>
            </ResponsiveContainer>
          </Panel>

          <Panel title="Tokens over time">
            <ResponsiveContainer width="100%" height={180}>
              <AreaChart data={data.series}>
                <XAxis dataKey="bucket" tickFormatter={fmtTime} tick={axisTick} stroke="currentColor" className="text-muted-foreground" />
                <YAxis tickFormatter={(v) => fmtTokens(v as number)} tick={axisTick} width={36} stroke="currentColor" className="text-muted-foreground" />
                <Tooltip content={<ChartTip labelFmt={fmtTime} fmt={(v: number) => fmtTokens(v)} />} />
                <Area type="monotone" dataKey="cache_read" stackId="1" stroke="#64748b" fill="#64748b" fillOpacity={0.15} strokeWidth={1} />
                <Area type="monotone" dataKey="in_tokens" stackId="1" stroke="#0891b2" fill="#0891b2" fillOpacity={0.2} strokeWidth={1} />
                <Area type="monotone" dataKey="out_tokens" stackId="1" stroke="#16a34a" fill="#16a34a" fillOpacity={0.25} strokeWidth={1.5} />
              </AreaChart>
            </ResponsiveContainer>
          </Panel>

          <Panel title="Tool calls" subtitle="click a tool to drill down">
            {data.tools.length === 0 ? (
              <Empty />
            ) : (
              <ResponsiveContainer width="100%" height={Math.max(120, data.tools.length * 26)}>
                <BarChart data={data.tools} layout="vertical" margin={{ left: 8 }}>
                  <XAxis type="number" tick={axisTick} stroke="currentColor" className="text-muted-foreground" />
                  <YAxis type="category" dataKey="name" width={92} tick={axisTick} stroke="currentColor" className="text-muted-foreground" />
                  <Tooltip content={<ChartTip />} cursor={{ fill: "currentColor", opacity: 0.05 }} />
                  <Bar dataKey="count" radius={3} cursor="pointer" onClick={(d: any) => setTool(d.name)}>
                    {data.tools.map((tt, i) => (
                      <Cell key={i} fill={tt.errors ? "#dc2626" : "#4f46e5"} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            )}
          </Panel>

          <Panel title="Model distribution">
            {data.models.length === 0 ? (
              <Empty />
            ) : (
              <div className="flex items-center gap-3">
                <ResponsiveContainer width="55%" height={180}>
                  <PieChart>
                    <Pie data={data.models} dataKey="count" nameKey="name" innerRadius={42} outerRadius={70} paddingAngle={2}>
                      {data.models.map((_, i) => (
                        <Cell key={i} fill={PIE[i % PIE.length]} />
                      ))}
                    </Pie>
                    <Tooltip content={<ChartTip />} />
                  </PieChart>
                </ResponsiveContainer>
                <ul className="flex-1 space-y-1 text-xs">
                  {data.models.map((m, i) => (
                    <li key={m.name} className="flex items-center gap-1.5">
                      <span className="h-2.5 w-2.5 rounded-sm" style={{ background: PIE[i % PIE.length] }} />
                      <span className="truncate">{shortModel(m.name)}</span>
                      <span className="ml-auto tabular-nums text-muted-foreground">{m.count}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </Panel>
        </div>

        <Panel title="Stop reasons">
          <div className="flex flex-wrap gap-2">
            {data.stop_reason.map((s) => (
              <span key={s.name} className="flex items-center gap-1.5 rounded-md bg-muted px-2 py-1 text-xs">
                <span className="font-medium">{s.name}</span>
                <span className="tabular-nums text-muted-foreground">{s.count}</span>
              </span>
            ))}
          </div>
        </Panel>
      </div>

      {tool && <ToolDrill name={tool} onClose={() => setTool(null)} onJump={onJump} />}
    </div>
  );
}

function ToolDrill({ name, onClose, onJump }: { name: string; onClose: () => void; onJump: (s: number, r?: number) => void }) {
  const [calls, setCalls] = useState<ToolCallRef[] | null>(null);
  useEffect(() => {
    api.toolCalls(name).then(setCalls);
  }, [name]);
  return (
    <div className="fixed inset-0 z-40 flex justify-end bg-black/40" onClick={onClose}>
      <div className="flex h-full w-[min(560px,90vw)] flex-col border-l bg-background shadow-xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b px-4 py-2.5">
          <div className="text-sm font-semibold">
            {name} <span className="text-muted-foreground">· {calls?.length ?? "…"} calls</span>
          </div>
          <button onClick={onClose} className="rounded-md p-1 hover:bg-accent">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {!calls ? (
            <div className="p-4 text-sm text-muted-foreground">Loading…</div>
          ) : (
            calls.map((c, i) => (
              <button
                key={i}
                onClick={() => onJump(c.session_id, c.request_id)}
                className="flex w-full items-start gap-2 border-b px-4 py-2 text-left hover:bg-muted/60"
              >
                <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{fmtTime(c.ts_start)}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{c.input_preview || c.tool_use_id}</span>
                <ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}

function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <Card className="px-3 py-2.5">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-xl font-semibold tabular-nums">{value}</div>
      {hint && <div className="text-[10px] text-muted-foreground">{hint}</div>}
    </Card>
  );
}

function Panel({ title, subtitle, children }: { title: string; subtitle?: string; children: React.ReactNode }) {
  return (
    <Card className="p-3">
      <div className="mb-2 flex items-baseline justify-between">
        <h3 className="text-xs font-semibold">{title}</h3>
        {subtitle && <span className="text-[10px] text-muted-foreground">{subtitle}</span>}
      </div>
      {children}
    </Card>
  );
}

function Empty() {
  return <div className="flex h-[120px] items-center justify-center text-xs text-muted-foreground">No data yet</div>;
}

const axisTick = { fontSize: 10, fill: "currentColor" };

function ChartTip({ active, payload, label, labelFmt, fmt }: any) {
  if (!active || !payload?.length) return null;
  return (
    <div className="rounded-md border bg-popover px-2 py-1.5 text-xs shadow-md">
      {label != null && <div className="mb-0.5 font-medium">{labelFmt ? labelFmt(label) : label}</div>}
      {payload.map((p: any) => (
        <div key={p.dataKey} className="flex items-center gap-1.5">
          <span className="h-2 w-2 rounded-sm" style={{ background: p.color || p.fill }} />
          <span className="text-muted-foreground">{p.name}</span>
          <span className="ml-auto font-medium tabular-nums">{fmt ? fmt(p.value) : p.value}</span>
        </div>
      ))}
    </div>
  );
}
