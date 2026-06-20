// Command cc-gateway is a transparent proxy between Claude Code and the
// Anthropic API that also records traffic into an embedded database and serves a
// web UI (trace explorer + analytics). The proxy path forwards every byte
// untouched; persistence runs off the hot path and can be disabled entirely.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"cc-gateway/internal/api"
	"cc-gateway/internal/ingest"
	"cc-gateway/internal/proxy"
	"cc-gateway/internal/store"
	"cc-gateway/internal/term"
	"cc-gateway/web"
)

var (
	host     = flag.String("host", "localhost", "interface to bind on (use 0.0.0.0 to expose, e.g. in a container)")
	port     = flag.String("port", "8443", "local port the proxy listens on")
	uiPort   = flag.String("ui-port", "8088", "local port the web UI/API listens on")
	dbPath   = flag.String("db", "cc-gateway.db", "path to the SQLite database")
	upstream = flag.String("upstream", "https://api.anthropic.com", "upstream Anthropic base URL")
	showBody = flag.Bool("body", false, "start with the formatted screen view on (the s toggle)")
	noColor  = flag.Bool("no-color", false, "disable ANSI colors")
	noUI     = flag.Bool("no-ui", false, "disable the web UI/API server")
	noStore  = flag.Bool("no-store", false, "disable persistence (pure transparent proxy)")
)

func main() {
	flag.Parse()

	up, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid -upstream: %v\n", err)
		os.Exit(1)
	}

	con := term.New(*noColor, *showBody)

	// displayHost is what we print in URLs; a wildcard bind is reached via
	// localhost from the same machine (or the mapped port from a container host).
	displayHost := *host
	if displayHost == "0.0.0.0" || displayHost == "" || displayHost == "::" {
		displayHost = "localhost"
	}

	// Persistence + ingest (optional).
	var sink *ingest.Sink
	var st *store.Store
	if !*noStore {
		st, err = store.Open(*dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open db: %v\n", err)
			os.Exit(1)
		}
		defer st.Close()
		// Recover token usage for any exchanges recorded before responses were
		// decoded properly. No-op once everything has been parsed.
		if n, berr := ingest.Backfill(st); berr != nil {
			fmt.Fprintf(os.Stderr, "backfill: %v\n", berr)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "backfilled usage for %d requests\n", n)
		}
		sink = ingest.NewSink(st, 1024)
		defer sink.Close()
	}

	// Web UI / API (optional; requires the store).
	uiURL := ""
	if !*noUI && st != nil {
		var bus *ingest.Broadcaster
		if sink != nil {
			bus = sink.Bus()
		}
		srv := api.New(st, bus, web.FS())
		uiURL = "http://" + displayHost + ":" + *uiPort
		go func() {
			s := &http.Server{Addr: *host + ":" + *uiPort, Handler: srv.Handler()}
			if err := s.ListenAndServe(); err != nil {
				fmt.Fprintf(os.Stderr, "ui server error: %v\n", err)
			}
		}()
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	client := &http.Client{Transport: transport} // no timeout: streaming runs for minutes

	h := proxy.New(up, client, con, sink)

	keys := con.StartKeyboard()
	con.Banner("http://"+displayHost+":"+*port, up.String(), uiURL, keys)

	srv := &http.Server{
		Addr:              *host + ":" + *port,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
