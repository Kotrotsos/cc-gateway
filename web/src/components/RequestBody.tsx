import { useEffect, useState, type ReactNode } from "react";
import * as Dialog from "@radix-ui/react-dialog";
import { Braces, ChevronDown, X } from "lucide-react";
import { api, type ContentBlock, type Message, type RequestDetail } from "@/lib/api";
import { cn, fmtCost, fmtTokens } from "@/lib/utils";
import { Badge, Button } from "./ui/primitives";

export function RequestBody({
  requestId,
  collapseNonce,
  baselineMessages,
}: {
  requestId: number;
  collapseNonce?: number;
  // Number of leading messages already shown by the previous request in this
  // continuation run. When > 0, the system prompt and those messages are folded
  // away so only the newly added turns show. 0/undefined = show everything.
  baselineMessages?: number;
}) {
  const [detail, setDetail] = useState<RequestDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .request(requestId)
      .then((d) => !cancelled && setDetail(d))
      .catch((e) => !cancelled && setErr(String(e)));
    return () => {
      cancelled = true;
    };
  }, [requestId]);

  if (err) return <div className="border-t px-3 py-2 text-xs text-destructive">{err}</div>;
  if (!detail) return <div className="border-t px-3 py-2 text-xs text-muted-foreground">Loading…</div>;

  const u = detail.response.usage;

  return (
    <div className="border-t">
      <div className="flex items-center gap-2 border-b bg-muted/40 px-3 py-1.5 text-[11px] text-muted-foreground">
        <span>{detail.model}</span>
        {detail.is_sse && <Badge variant="outline">SSE</Badge>}
        {detail.stop_reason && <span>stop: {detail.stop_reason}</span>}
        <span className="ml-auto tabular-nums">
          in {fmtTokens(u.input_tokens)} · out {fmtTokens(u.output_tokens)} · cache {fmtTokens(u.cache_read_input_tokens)} ·{" "}
          {fmtCost(detail.est_cost)}
        </span>
        <RawDialog requestId={requestId} />
      </div>

      {(() => {
        const baseline = Math.min(baselineMessages ?? 0, detail.messages.length);
        const folded = baseline > 0;
        const prior = folded ? detail.messages.slice(0, baseline) : [];
        const fresh = folded ? detail.messages.slice(baseline) : detail.messages;
        return (
          // Remounting on collapseNonce resets every TextBlock's local "more" state.
          <div key={collapseNonce} className="space-y-2.5 px-3 py-3">
            {folded ? (
              <PriorContext system={detail.system} prior={prior} />
            ) : (
              detail.system.length > 0 && <Turn role="system" content={detail.system} />
            )}
            {fresh.map((m, i) => (
              <Turn key={i} role={m.role} content={m.content} />
            ))}
            {folded && fresh.length === 0 && (
              <div className="text-xs text-muted-foreground">No new messages — this request resent the same context.</div>
            )}
            {detail.response.ok && <Turn role="assistant" content={detail.response.content} highlight />}
            {detail.error && <div className="text-xs text-destructive">error: {detail.error}</div>}
          </div>
        );
      })()}
    </div>
  );
}

const roleStyles: Record<string, string> = {
  system: "text-violet-600 dark:text-violet-400",
  user: "text-sky-600 dark:text-sky-400",
  assistant: "text-emerald-600 dark:text-emerald-400",
};

function Turn({ role, content, highlight }: { role: string; content: ContentBlock[]; highlight?: boolean }) {
  return (
    <div className={cn("rounded-md border", highlight ? "border-emerald-500/20 bg-emerald-500/[0.03]" : "bg-background")}>
      <div className={cn("px-2.5 pt-1.5 text-[11px] font-semibold uppercase tracking-wide", roleStyles[role] ?? "text-muted-foreground")}>
        {role}
      </div>
      <div className="space-y-1.5 px-2.5 pb-2 pt-1">
        {content.map((b, i) => (
          <Block key={i} b={b} />
        ))}
      </div>
    </div>
  );
}

// PriorContext folds the conversation prefix that earlier requests already
// showed (the system prompt plus messages carried over) into one collapsible
// block, so a continuation foregrounds only its newly added turns.
function PriorContext({ system, prior }: { system: ContentBlock[]; prior: Message[] }) {
  const [open, setOpen] = useState(false);
  const count = prior.length + (system.length > 0 ? 1 : 0);
  return (
    <div className="rounded-md border border-dashed">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-left text-[11px] font-medium text-muted-foreground hover:text-foreground"
      >
        <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", !open && "-rotate-90")} />
        {open ? "Hide" : "Show"} earlier context · {count} item{count === 1 ? "" : "s"} already shown above
      </button>
      {open && (
        <div className="space-y-1.5 border-t px-2 py-2">
          {system.length > 0 && <Turn role="system" content={system} />}
          {prior.map((m, i) => (
            <Turn key={i} role={m.role} content={m.content} />
          ))}
        </div>
      )}
    </div>
  );
}

function Block({ b }: { b: ContentBlock }) {
  switch (b.type) {
    case "text":
      return <TextBlock text={b.text ?? ""} />;
    case "thinking":
      return (
        <div className="rounded bg-muted/60 px-2 py-1">
          <div className="mb-0.5 text-[10px] font-medium uppercase text-muted-foreground">thinking</div>
          <TextBlock text={b.thinking ?? ""} muted />
        </div>
      );
    case "tool_use":
      return (
        <div className="rounded border border-amber-500/20 bg-amber-500/[0.05] px-2 py-1">
          <div className="flex items-center gap-1.5 text-xs">
            <Braces className="h-3 w-3 text-amber-600 dark:text-amber-400" />
            <span className="font-semibold">{b.name}</span>
            <span className="text-muted-foreground">{b.id}</span>
          </div>
          <ToolInput input={b.input} />
        </div>
      );
    case "tool_result":
      return (
        <div className={cn("rounded border px-2 py-1", b.is_error ? "border-destructive/30 bg-destructive/[0.05]" : "border-border bg-muted/40")}>
          <div className="mb-0.5 flex items-center gap-1.5 text-[10px] font-medium uppercase text-muted-foreground">
            tool_result {b.is_error && <span className="text-destructive">error</span>}
          </div>
          <TextBlock text={resultToText(b.content)} muted mono />
        </div>
      );
    case "image":
      return <div className="text-xs text-muted-foreground">[image]</div>;
    default:
      return <div className="text-xs text-muted-foreground">[{b.type}]</div>;
  }
}

// TextBlock shows the first two lines and expands on click when there is more.
function TextBlock({ text, muted, mono }: { text: string; muted?: boolean; mono?: boolean }) {
  const [open, setOpen] = useState(false);
  const lines = text.replace(/\r/g, "").replace(/\n+$/, "").split("\n");
  const hasMore = lines.length > 2 || text.length > 240;
  const shown = open ? text : truncate(lines.slice(0, 2).join("\n"), 240);
  if (!text.trim()) return null;
  return (
    <div className={cn("group whitespace-pre-wrap break-words text-xs leading-relaxed", muted && "text-muted-foreground", mono && "font-mono")}>
      {mono ? shown : renderInlineCode(shown)}
      {hasMore && (
        <button onClick={() => setOpen((v) => !v)} className="ml-1 inline-flex items-center gap-0.5 align-baseline text-[11px] font-medium text-primary hover:underline">
          {open ? "less" : "more"}
          <ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
        </button>
      )}
    </div>
  );
}

function ToolInput({ input }: { input: unknown }) {
  if (input == null) return null;
  if (typeof input === "object" && !Array.isArray(input)) {
    const entries = Object.entries(input as Record<string, unknown>);
    return (
      <div className="mt-1 space-y-0.5">
        {entries.map(([k, v]) => (
          <div key={k} className="flex gap-1.5 text-xs">
            <span className="shrink-0 font-medium text-muted-foreground">{k}</span>
            <TextBlock text={typeof v === "string" ? v : JSON.stringify(v)} mono />
          </div>
        ))}
      </div>
    );
  }
  return <TextBlock text={JSON.stringify(input)} mono />;
}

function RawDialog({ requestId }: { requestId: number }) {
  const [raw, setRaw] = useState<RequestDetail | null>(null);
  return (
    <Dialog.Root onOpenChange={(o) => o && !raw && api.request(requestId, true).then(setRaw)}>
      <Dialog.Trigger asChild>
        <Button variant="ghost" size="sm" className="h-6 px-1.5 text-[11px]">
          <Braces className="h-3 w-3" /> Raw
        </Button>
      </Dialog.Trigger>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/40" />
        <Dialog.Content className="fixed right-0 top-0 z-50 flex h-full w-[min(720px,90vw)] flex-col border-l bg-background shadow-xl focus:outline-none">
          <div className="flex items-center justify-between border-b px-4 py-2.5">
            <Dialog.Title className="text-sm font-semibold">Raw exchange · request #{requestId}</Dialog.Title>
            <Dialog.Close asChild>
              <Button variant="ghost" size="icon">
                <X className="h-4 w-4" />
              </Button>
            </Dialog.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-4">
            {!raw ? (
              <div className="text-sm text-muted-foreground">Loading…</div>
            ) : (
              <>
                <RawSection title="Request body" body={raw.raw_request} />
                <RawSection title="Response body" body={raw.raw_response} />
              </>
            )}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function RawSection({ title, body }: { title: string; body?: string }) {
  return (
    <div className="mb-4">
      <div className="mb-1 text-xs font-semibold text-muted-foreground">{title}</div>
      <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 text-[11px] leading-relaxed">{prettyMaybe(body ?? "")}</pre>
    </div>
  );
}

function prettyMaybe(s: string): string {
  const t = s.trim();
  if (t.startsWith("{") || t.startsWith("[")) {
    try {
      return JSON.stringify(JSON.parse(t), null, 2);
    } catch {
      // SSE or non-JSON: show as-is
    }
  }
  return s;
}

function resultToText(content: unknown): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) {
    return content
      .map((b: any) => (typeof b === "string" ? b : b?.text ? b.text : b?.type ? `[${b.type}]` : ""))
      .join("\n");
  }
  if (content == null) return "";
  return JSON.stringify(content);
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}

// renderInlineCode lightly styles `inline` and ```fenced``` code spans so code
// stands out from surrounding prose. The parent keeps whitespace-pre-wrap, so
// fenced blocks retain their newlines.
function renderInlineCode(text: string): ReactNode {
  const re = /```([\s\S]*?)```|`([^`\n]+)`/g;
  const parts: ReactNode[] = [];
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    if (m[1] !== undefined) {
      // Drop an optional language hint on the first line and trim edge newlines.
      const code = m[1].replace(/^[a-zA-Z0-9+#._-]*\n/, "").replace(/^\n+|\n+$/g, "");
      parts.push(
        <code key={key++} className="my-0.5 block overflow-x-auto rounded-md border bg-muted/60 px-2 py-1 font-mono text-[0.92em] text-foreground">
          {code}
        </code>,
      );
    } else {
      parts.push(
        <code key={key++} className="rounded border bg-muted/70 px-1 font-mono text-[0.9em] text-foreground">
          {m[2]}
        </code>,
      );
    }
    last = re.lastIndex;
  }
  if (last < text.length) parts.push(text.slice(last));
  return parts;
}
