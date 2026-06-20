.PHONY: build ui dev-api dev-ui test fmt clean docker docker-up docker-down

# Build the web UI then the single self-contained binary that embeds it.
build: ui
	go build -o cc-gateway ./cmd/cc-gateway

# Build the React app into web/dist (which the binary embeds via go:embed).
ui:
	pnpm --dir web install
	pnpm --dir web build

# Dev: run the Go proxy + API. The UI is served from the embedded build; for
# live frontend reloads run `make dev-ui` in a second terminal and open :5173.
dev-api:
	go run ./cmd/cc-gateway

dev-ui:
	pnpm --dir web dev

test:
	go test ./...

fmt:
	gofmt -w cmd internal web

clean:
	rm -f cc-gateway cc-gateway.db cc-gateway.db-wal cc-gateway.db-shm

# Build the self-contained Docker image (UI + binary, no toolchain needed).
docker:
	docker build -t cc-gateway .

# Super-easy deploy: build and run in the background with a persistent DB volume.
docker-up:
	docker compose up -d --build

docker-down:
	docker compose down
