import { useEffect, useRef, useState } from "react";
import type { LiveEvent } from "./api";

export interface LiveState {
  connected: boolean;
  events: LiveEvent[];
  // version bumps on every new event so consumers can cheaply re-fetch.
  version: number;
  last: LiveEvent | null;
}

// useLive opens a single SSE connection to /api/stream and exposes the most
// recent events plus a monotonically increasing version. Reconnects on drop.
export function useLive(enabled: boolean): LiveState {
  const [state, setState] = useState<LiveState>({ connected: false, events: [], version: 0, last: null });
  const esRef = useRef<EventSource | null>(null);

  useEffect(() => {
    if (!enabled) {
      esRef.current?.close();
      esRef.current = null;
      setState((s) => ({ ...s, connected: false }));
      return;
    }
    const es = new EventSource("/api/stream");
    esRef.current = es;
    es.onopen = () => setState((s) => ({ ...s, connected: true }));
    es.onerror = () => setState((s) => ({ ...s, connected: false }));
    es.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data) as LiveEvent;
        setState((s) => ({
          connected: true,
          events: [ev, ...s.events].slice(0, 200),
          version: s.version + 1,
          last: ev,
        }));
      } catch {
        // ignore malformed
      }
    };
    return () => {
      es.close();
      esRef.current = null;
    };
  }, [enabled]);

  return state;
}
