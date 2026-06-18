# cc-gateway

A transparent, zero-modification proxy that sits between Claude Code and the
Anthropic API so you can **see the traffic flow**. It does not filter, strip,
rewrite, or store anything. Every byte is forwarded untouched, so streaming,
tool use, prompt caching, and your existing auth all keep working exactly as
before. The gateway just prints an intuitive view of what goes past.

## Build

Single static binary, standard library only:

```sh
go build -o cc-gateway .
```

## Run

```sh
./cc-gateway
```

It listens on `http://localhost:8443` and forwards to
`https://api.anthropic.com`. On startup it prints exactly how to point Claude
Code at it:

```sh
export ANTHROPIC_BASE_URL=http://localhost:8443
claude
```

or for a single run:

```sh
ANTHROPIC_BASE_URL=http://localhost:8443 claude
```

Your existing OAuth token (subscription) or `ANTHROPIC_API_KEY` passes straight
through, so you don't need to configure credentials in the gateway.

## What you see

For every request the gateway prints a request block and, when it completes, a
response block:

```
┌─ → #1  REQUEST 16:41:59.943
│  POST /v1/messages
│  model claude-opus-4-8   stream true   max_tokens 1024
│  1 messages   2 tools   system prompt   172 B body
└─
┌─ ← #1  RESPONSE 16:41:59.958
│  200 OK   text/event-stream   16ms
│  flow: message_start → content_block_start → content_block_delta ×5 → message_delta → message_stop
│  tokens: in 4201 / out 532   cache: read 3800 / write 0
│  stop_reason: end_turn
└─
```

- **flow** is the live shape of the SSE stream (the event types and how many of
  each), so you can read the anatomy of a streamed completion at a glance.
- **tokens / cache** are pulled read-only from the stream's own usage events.

## Live hotkeys

While the gateway is running in an interactive terminal, two single-key toggles
let you go from summaries to the full content on demand (no restart needed):

| key | toggle                                                                  |
|-----|-------------------------------------------------------------------------|
| `s` | show the conversation **formatted on screen** (messages, not JSON)      |
| `f` | record the **raw JSON** request and response bodies **to a file**       |

Press once to turn on, press again to turn off.

The `s` view is built for reading, not grepping: the system prompt, each message
(with its real line breaks), tool calls rendered as `key: value`, tool results,
extended-thinking blocks, and a token / `stop_reason` line. Streamed responses
are reassembled from their SSE deltas back into the message the model actually
sent. Images are shown as `[image]` rather than dumping base64. Anything that
isn't a Messages payload (errors, other endpoints) falls back to pretty JSON.

```
╞══ #12  REQUEST  POST /v1/messages
  system
    You are Claude Code.
  user
    fix the failing test
  assistant
    Let me look at it.
    tool_use: Read (toolu_01abc)
      file_path: /repo/main_test.go
  user
    tool_result (toolu_01abc ok)
      --- FAIL: TestParse ...
╰──
```

The `f` recording is the opposite: a verbatim, ANSI-free transcript of the
exact bytes on the wire, so it round-trips and can be replayed or diffed. Each
`f` session opens a fresh `cc-gateway-<timestamp>.log` and closes it on toggle
off.

Toggles take effect from the next request; a response already mid-stream is not
captured retroactively. Hotkeys are disabled automatically when stdin is not a
terminal (e.g. when output is piped). `-body` simply sets the initial state of
the `s` toggle.

## Flags

| flag         | default                     | meaning                              |
|--------------|-----------------------------|--------------------------------------|
| `-port`      | `8443`                      | local port to listen on              |
| `-upstream`  | `https://api.anthropic.com` | upstream base URL                    |
| `-body`      | `false`                     | start with the formatted screen view on (the `s` toggle) |
| `-no-color`  | `false`                     | disable ANSI colors                  |

`HTTPS_PROXY` / `HTTP_PROXY` are honored for the outbound connection.

## How it works

Plain HTTP listener on localhost (no TLS certs needed since Claude Code connects
over loopback). Each request is buffered just enough to summarize it, then
replayed to the upstream with identical method, path, query, headers, and body.
The response is streamed back chunk-by-chunk with an immediate flush per chunk,
and the same bytes are tapped read-only to build the summary. Hop-by-hop headers
are dropped per the HTTP spec; everything else is forwarded verbatim.
