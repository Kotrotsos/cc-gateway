# syntax=docker/dockerfile:1

# 1. Build the React UI into web/dist. Runs on the native build platform (not the
#    target arch) so multi-arch builds don't emulate this heavy step.
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /app/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# 2. Build the single static Go binary that embeds the UI. The SQLite driver is
#    pure Go (modernc), so CGO stays off and we cross-compile to the target arch
#    natively — fast, no QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /cc-gateway ./cmd/cc-gateway
RUN mkdir -p /data

# 3. Minimal runtime: just the binary plus a writable /data for the database.
FROM gcr.io/distroless/static-debian12 AS run
COPY --from=build /cc-gateway /cc-gateway
COPY --from=build /data /data
VOLUME /data
# 8443: the proxy Claude Code points at. 8088: the web UI / API.
EXPOSE 8443 8088
ENTRYPOINT ["/cc-gateway"]
CMD ["-host", "0.0.0.0", "-db", "/data/cc-gateway.db"]
