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

## Flags

| flag         | default                     | meaning                              |
|--------------|-----------------------------|--------------------------------------|
| `-port`      | `8443`                      | local port to listen on              |
| `-upstream`  | `https://api.anthropic.com` | upstream base URL                    |
| `-body`      | `false`                     | also dump full request/response bodies |
| `-no-color`  | `false`                     | disable ANSI colors                  |

`HTTPS_PROXY` / `HTTP_PROXY` are honored for the outbound connection.

## How it works

Plain HTTP listener on localhost (no TLS certs needed since Claude Code connects
over loopback). Each request is buffered just enough to summarize it, then
replayed to the upstream with identical method, path, query, headers, and body.
The response is streamed back chunk-by-chunk with an immediate flush per chunk,
and the same bytes are tapped read-only to build the summary. Hop-by-hop headers
are dropped per the HTTP spec; everything else is forwarded verbatim.
