import { useState } from "react";
import { Activity, BarChart3, GitBranch, Moon, Radio, Sun } from "lucide-react";
import { Button } from "./components/ui/primitives";
import { useTheme } from "./lib/theme";
import { useLive } from "./lib/live";
import { cn } from "./lib/utils";
import { Explorer } from "./components/Explorer";
import { AnalyticsView } from "./components/Analytics";

export type Focus = { sessionId: number; requestId?: number } | null;

export function App() {
  const [tab, setTab] = useState<"explorer" | "analytics">("explorer");
  const [live, setLive] = useState(true);
  const [focus, setFocus] = useState<Focus>(null);
  const liveState = useLive(live);
  const { theme, toggle } = useTheme();

  function jumpToTrace(sessionId: number, requestId?: number) {
    setFocus({ sessionId, requestId });
    setTab("explorer");
  }

  return (
    <div className="flex h-screen flex-col">
      <header className="flex h-12 shrink-0 items-center gap-4 border-b px-4">
        <div className="flex items-center gap-2 font-semibold tracking-tight">
          <GitBranch className="h-4 w-4 text-primary" />
          cc-gateway
        </div>

        <nav className="flex items-center gap-1">
          <TabButton active={tab === "explorer"} onClick={() => setTab("explorer")} icon={<Activity className="h-3.5 w-3.5" />}>
            Explorer
          </TabButton>
          <TabButton active={tab === "analytics"} onClick={() => setTab("analytics")} icon={<BarChart3 className="h-3.5 w-3.5" />}>
            Analytics
          </TabButton>
        </nav>

        <div className="ml-auto flex items-center gap-2">
          <button
            onClick={() => setLive((v) => !v)}
            className={cn(
              "flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-medium transition-colors",
              live ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400" : "text-muted-foreground",
            )}
            title={live ? "Live tail on" : "Live tail off"}
          >
            <Radio className={cn("h-3.5 w-3.5", live && liveState.connected && "animate-pulse")} />
            {live ? (liveState.connected ? "Live" : "Connecting") : "Paused"}
          </button>
          <Button variant="ghost" size="icon" onClick={toggle} title="Toggle theme">
            {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
          </Button>
        </div>
      </header>

      <main className="min-h-0 flex-1">
        {tab === "explorer" ? (
          <Explorer live={liveState} focus={focus} clearFocus={() => setFocus(null)} />
        ) : (
          <AnalyticsView onJump={jumpToTrace} />
        )}
      </main>
    </div>
  );
}

function TabButton({
  active,
  onClick,
  icon,
  children,
}: {
  active: boolean;
  onClick: () => void;
  icon: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "flex items-center gap-1.5 rounded-md px-2.5 py-1 text-sm font-medium transition-colors",
        active ? "bg-accent text-foreground" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {icon}
      {children}
    </button>
  );
}
