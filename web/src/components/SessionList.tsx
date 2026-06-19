import { AlertTriangle } from "lucide-react";
import type { SessionSummary } from "@/lib/api";
import { cn, fmtCost, fmtRelative, fmtTokens, shortModel } from "@/lib/utils";

export function SessionRow({ s, active, onClick }: { s: SessionSummary; active: boolean; onClick: () => void }) {
  const project = s.cwd ? s.cwd.split("/").filter(Boolean).pop() : s.session_key.replace(/^s_/, "").slice(0, 8);
  return (
    <button
      onClick={onClick}
      className={cn(
        "block w-full border-b px-3 py-2.5 text-left transition-colors",
        active ? "bg-accent" : "hover:bg-muted/60",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium">{project || "session"}</span>
        <span className="shrink-0 text-xs text-muted-foreground">{fmtRelative(s.last_seen)}</span>
      </div>
      <div className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
        {s.model && <span className="rounded bg-muted px-1 py-px font-medium">{shortModel(s.model)}</span>}
        {s.git_branch && <span className="truncate">{s.git_branch}</span>}
        {s.error_count > 0 && (
          <span className="flex items-center gap-0.5 text-destructive">
            <AlertTriangle className="h-3 w-3" />
            {s.error_count}
          </span>
        )}
      </div>
      <div className="mt-1.5 flex items-center gap-3 text-[11px] tabular-nums text-muted-foreground">
        <span>{s.num_requests} req</span>
        <span>↑{fmtTokens(s.in_tokens)}</span>
        <span>↓{fmtTokens(s.out_tokens)}</span>
        <span className="ml-auto font-medium text-foreground/70">{fmtCost(s.est_cost)}</span>
      </div>
    </button>
  );
}
