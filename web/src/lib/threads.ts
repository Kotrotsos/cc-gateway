import type { RequestSummary } from "./api";

// continuationRuns splits requests (in display order) into runs of consecutive
// requests that continue the same conversation: same thread_key and a
// non-shrinking message count, meaning each one just appends to the previous
// one's history. A different thread, or a message count that drops (a context
// reset / compaction), ends the run and starts a new one. The first request in
// a run carries the full context; the rest only add new turns.
export function continuationRuns(requests: RequestSummary[]): RequestSummary[][] {
  const runs: RequestSummary[][] = [];
  for (const r of requests) {
    const run = runs[runs.length - 1];
    const prev = run && run[run.length - 1];
    const sameThread = prev && (prev.thread_key || "_") === (r.thread_key || "_");
    const grows = prev && r.num_messages >= prev.num_messages;
    if (prev && sameThread && grows) run.push(r);
    else runs.push([r]);
  }
  return runs;
}

export interface Thread {
  key: string;
  label: string; // "main" or "subagent N"
  isMain: boolean;
  requests: RequestSummary[];
  peak: RequestSummary; // the request with the largest prompt (context high-water)
  peakTotal: number;
  minSeq: number;
}

const total = (r: RequestSummary) => r.in_tokens + r.cache_read + r.cache_write;

// groupThreads splits a session's requests into conversation threads by
// thread_key. A Claude Code session interleaves the main thread with any
// subagents it spawns; the thread whose first request comes earliest is the
// main one, the rest are subagents in order of first appearance. Requests
// without a key (older data) collapse into a single thread.
export function groupThreads(requests: RequestSummary[]): Thread[] {
  const byKey = new Map<string, RequestSummary[]>();
  for (const r of requests) {
    const k = r.thread_key || "_";
    const arr = byKey.get(k);
    if (arr) arr.push(r);
    else byKey.set(k, [r]);
  }

  const threads = [...byKey.entries()].map(([key, reqs]) => {
    let peak = reqs[0];
    let peakTotal = 0;
    let minSeq = Infinity;
    for (const r of reqs) {
      const t = total(r);
      if (t >= peakTotal) {
        peakTotal = t;
        peak = r;
      }
      if (r.seq < minSeq) minSeq = r.seq;
    }
    return { key, label: "", isMain: false, requests: reqs, peak, peakTotal, minSeq };
  });

  threads.sort((a, b) => a.minSeq - b.minSeq);
  let sub = 0;
  for (let i = 0; i < threads.length; i++) {
    threads[i].isMain = i === 0;
    threads[i].label = i === 0 ? "main" : `subagent ${++sub}`;
  }
  return threads;
}
