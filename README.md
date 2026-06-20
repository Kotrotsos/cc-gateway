# cc-gateway

A transparent proxy between Claude Code and the Anthropic API that makes the
traffic **visible and browsable**. It forwards every byte untouched, so
streaming, tool use, prompt caching, and your existing auth all keep working
exactly as before, and on the side it records each exchange into an embedded
database and serves a web UI: a trace explorer and live analytics. Think
Langfuse / LangSmith, but for Claude Code, in a single binary.

The proxy path is sacred: persistence runs entirely off the hot path
(asynchronous, best-effort, panic-isolated). If the database is slow, locked, or
disabled, forwarding is unaffected. `-no-store` turns persistence off entirely
for the original pure-proxy behavior.

## Quick start

No clone, no build. Pick one:

**Docker** (only needs Docker installed):

```sh
docker run -d --name cc-gateway \
  -p 8443:8443 -p 8088:8088 -v cc-gateway:/data \
  ghcr.io/kotrotsos/cc-gateway
```

**Binary** (macOS / Linux):

```sh
curl -fsSL https://raw.githubusercontent.com/Kotrotsos/cc-gateway/main/install.sh | sh
cc-gateway
```

Then point Claude Code at the proxy and open the UI:

```sh
export ANTHROPIC_BASE_URL=http://localhost:8443
claude
```

- Web UI: <http://localhost:8088>
- Your existing Claude subscription token / `ANTHROPIC_API_KEY` passes straight through.

> Keep it local. The UI is unauthenticated and stores full prompts/code/tool
> output; don't expose `:8088` on the open internet.

## What you get

- **Explorer** — every Claude Code session as one trace from start to finish.
  Each API request is a node in a timeline; reconstructed tool-call spans sit
  between nodes with their durations. Expand any request to read the
  conversation: system prompt, messages, tool calls and results, extended
  thinking — each block collapsible with a two-line preview. A raw-JSON drawer
  shows the verbatim bytes.
- **Analytics** — totals (sessions, requests, tokens, cache hit rate, estimated
  cost, tool calls, latency), requests/tokens over time, a tool-type breakdown
  you can drill into (click a tool → list its calls → jump to the originating
  trace), model distribution and stop-reason breakdown.
- **Live tail** — new exchanges stream into the UI over SSE as they happen.

## Build

The binary embeds the built web UI, so the only build dependency beyond Go is a
JS toolchain (Node + pnpm) to compile the frontend once.

```sh
make build      # builds web/dist then the single ./cc-gateway binary
```

Pure Go otherwise: the database is `modernc.org/sqlite` (no cgo), so the result
is a single static, cross-compilable binary.

## Run

```sh
./cc-gateway
```

- Proxy: `http://localhost:8443` → forwards to `https://api.anthropic.com`
- Web UI: `http://localhost:8088`

Point Claude Code at the proxy:

```sh
export ANTHROPIC_BASE_URL=http://localhost:8443
claude
```

Your existing OAuth token (subscription) or `ANTHROPIC_API_KEY` passes straight
through; the gateway needs no credentials. Open the web UI and watch the session
appear live.

## Deploy

The whole thing is one static binary plus a SQLite file, so deploying is just
running it somewhere with a persistent volume. No toolchain needed on the host.

**Docker (recommended):**

```sh
make docker-up        # docker compose up -d --build
# or: docker compose up -d --build
```

That builds the image (UI + binary), runs it detached, and persists the
database in a named `ccdata` volume. Then:

- Proxy: `http://<host>:8443` — set `ANTHROPIC_BASE_URL` to this
- Web UI: `http://<host>:8088`

`make docker-down` stops it; the volume (and your recorded history) survives.

Because the image is a single static binary, the same `Dockerfile` deploys
unchanged to any container host (Fly.io, Railway, Render, a plain VPS). The
binary binds `0.0.0.0` inside the container via `-host`; locally it still
defaults to `localhost`.

> **Heads up before exposing it publicly.** The web UI has no authentication and
> the database records full request/response bodies (your prompts, code, tool
> output). Keep it on a private network, behind your own auth proxy, or bound to
> `localhost` / a VPN. Don't put the UI on the open internet as-is.

## Development

```sh
make dev-api    # terminal 1: Go proxy + API on :8443 / :8088
make dev-ui     # terminal 2: Vite dev server on :5173 (proxies /api → :8088)
```

Use `http://localhost:5173` for hot-reloading frontend work; the embedded build
at `:8088` is what ships.

## Live hotkeys (terminal view)

The original terminal view is still there. Two single-key toggles work while the
gateway runs in an interactive terminal:

| key | toggle                                                             |
|-----|--------------------------------------------------------------------|
| `s` | show the conversation **formatted on screen** (messages, not JSON) |
| `f` | record the **raw JSON** request/response bodies **to a file**      |

## Flags

| flag         | default                     | meaning                                   |
|--------------|-----------------------------|-------------------------------------------|
| `-host`      | `localhost`                 | bind interface (`0.0.0.0` to expose)      |
| `-port`      | `8443`                      | proxy listen port                         |
| `-ui-port`   | `8088`                      | web UI / API listen port                  |
| `-db`        | `cc-gateway.db`             | SQLite database path                      |
| `-upstream`  | `https://api.anthropic.com` | upstream base URL                         |
| `-no-ui`     | `false`                     | disable the web UI / API server           |
| `-no-store`  | `false`                     | disable persistence (pure transparent proxy) |
| `-body`      | `false`                     | start with the formatted screen view on   |
| `-no-color`  | `false`                     | disable ANSI colors                       |

`HTTPS_PROXY` / `HTTP_PROXY` are honored for the outbound connection.

## How it works

A plain HTTP listener on localhost replays each request to the upstream with
identical method, path, query, headers and body, and streams the response back
chunk-by-chunk with an immediate flush so tokens arrive in real time. The same
bytes are tapped read-only.

When persistence is on, the finished exchange is handed to a bounded channel and
a single writer goroutine parses it and stores it. Request/response bodies are
content-addressed and gzip-compressed, so the large system prompt Claude Code
resends on every request (prompt caching) is stored only once. Sessions are
correlated from the `metadata.user_id` Claude Code sends; tool spans are
reconstructed by matching each `tool_use` to the `tool_result` that answers it
in the following request.

### Layout

```
cmd/cc-gateway      wiring, flags, startup banner
internal/proxy      transparent forwarding + SSE tap
internal/parse      structured parse of requests/responses + SSE reassembly
internal/term       terminal view + s/f hotkeys
internal/store      SQLite schema, blob dedup, queries, analytics
internal/ingest     async capture, session correlation, span building
internal/api        REST API + live SSE stream + embedded UI
web/                React + Tailwind + shadcn UI (embedded as web/dist)
```
