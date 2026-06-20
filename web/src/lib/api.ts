// API client and types mirroring the Go store DTOs.

export interface SessionSummary {
  id: number;
  session_key: string;
  first_seen: number;
  last_seen: number;
  model: string;
  cwd: string;
  git_branch: string;
  cli_version: string;
  num_requests: number;
  in_tokens: number;
  out_tokens: number;
  cache_read: number;
  cache_write: number;
  est_cost: number;
  error_count: number;
}

export interface ToolUseRef {
  tool_use_id: string;
  name: string;
  input_preview: string;
}

export interface RequestSummary {
  id: number;
  seq: number;
  thread_key: string;
  ts_start: number;
  ts_end: number;
  duration_ms: number;
  status: number;
  model: string;
  in_tokens: number;
  out_tokens: number;
  cache_read: number;
  cache_write: number;
  est_cost: number;
  stop_reason: string;
  num_tools: number;
  error: string;
  assistant_preview: string;
  tool_uses: ToolUseRef[];
}

export interface Span {
  tool_use_id: string;
  name: string;
  call_request_id: number;
  result_request_id: number;
  started: number;
  ended: number;
  duration_ms: number;
  is_error: boolean;
  has_result: boolean;
  input_preview: string;
}

export interface SessionDetail {
  session: SessionSummary;
  requests: RequestSummary[];
  spans: Span[];
}

export interface ContentBlock {
  type: string;
  text?: string;
  thinking?: string;
  id?: string;
  name?: string;
  input?: unknown;
  tool_use_id?: string;
  content?: unknown;
  is_error?: boolean;
}

export interface Message {
  role: string;
  content: ContentBlock[];
}

export interface Usage {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
}

export interface RequestDetail {
  id: number;
  session_id: number;
  seq: number;
  ts_start: number;
  ts_end: number;
  duration_ms: number;
  method: string;
  path: string;
  status: number;
  model: string;
  is_sse: boolean;
  truncated: boolean;
  stop_reason: string;
  usage: Usage;
  est_cost: number;
  error: string;
  system: ContentBlock[];
  messages: Message[];
  response: { ok: boolean; model?: string; content: ContentBlock[]; stop_reason?: string; usage: Usage };
  raw_request?: string;
  raw_response?: string;
}

export interface NameCount {
  name: string;
  count: number;
  errors?: number;
  tokens?: number;
}

export interface TimePoint {
  bucket: number;
  requests: number;
  in_tokens: number;
  out_tokens: number;
  cache_read: number;
}

export interface Analytics {
  totals: {
    sessions: number;
    requests: number;
    in_tokens: number;
    out_tokens: number;
    cache_read: number;
    cache_write: number;
    est_cost: number;
    tool_calls: number;
    errors: number;
    avg_latency_ms: number;
  };
  tools: NameCount[];
  models: NameCount[];
  stop_reason: NameCount[];
  series: TimePoint[];
  cache_hit_rate: number;
}

export interface ToolCallRef {
  request_id: number;
  session_id: number;
  ts_start: number;
  tool_use_id: string;
  input_preview: string;
}

export interface LiveEvent {
  type: string;
  session_id: number;
  request_id: number;
  seq: number;
  session_key: string;
  ts_start: number;
  duration_ms: number;
  status: number;
  model: string;
  in_tokens: number;
  out_tokens: number;
  num_tools: number;
  est_cost: number;
  error: string;
}

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json() as Promise<T>;
}

export const api = {
  sessions: (params: Record<string, string> = {}) =>
    get<SessionSummary[]>("/api/sessions?" + new URLSearchParams(params).toString()),
  session: (id: number) => get<SessionDetail>(`/api/sessions/${id}`),
  request: (id: number, raw = false) => get<RequestDetail>(`/api/requests/${id}${raw ? "?raw=1" : ""}`),
  analytics: (params: Record<string, string> = {}) =>
    get<Analytics>("/api/analytics?" + new URLSearchParams(params).toString()),
  toolCalls: (name: string) => get<ToolCallRef[]>(`/api/analytics/tools/${encodeURIComponent(name)}`),
  models: () => get<string[]>("/api/models"),
  search: (q: string) => get<{ request_id: number; session_id: number; snippet: string }[]>(`/api/search?q=${encodeURIComponent(q)}`),
};
