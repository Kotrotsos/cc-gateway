// Package store is the persistence layer: an embedded SQLite database (pure-Go
// modernc driver, WAL mode, FTS5) that records every captured exchange. Request
// and response bodies are content-addressed and gzip-compressed so the large
// system prompt Claude Code resends on every request is stored only once.
package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the database handle. All writes go through a single goroutine
// (the ingest writer), so the connection needs no external locking; reads from
// the API run concurrently and SQLite's WAL mode keeps them non-blocking.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS blobs (
  hash      TEXT PRIMARY KEY,
  gz        BLOB NOT NULL,
  raw_size  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id          INTEGER PRIMARY KEY,
  session_key TEXT UNIQUE NOT NULL,
  first_seen  INTEGER NOT NULL,
  last_seen   INTEGER NOT NULL,
  model       TEXT,
  cwd         TEXT,
  git_branch  TEXT,
  cli_version TEXT,
  num_requests INTEGER NOT NULL DEFAULT 0,
  in_tokens   INTEGER NOT NULL DEFAULT 0,
  out_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_read  INTEGER NOT NULL DEFAULT 0,
  cache_write INTEGER NOT NULL DEFAULT 0,
  est_cost    REAL NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS requests (
  id           INTEGER PRIMARY KEY,
  session_id   INTEGER NOT NULL REFERENCES sessions(id),
  seq          INTEGER NOT NULL,
  ts_start     INTEGER NOT NULL,
  ts_end       INTEGER NOT NULL,
  duration_ms  INTEGER NOT NULL,
  method       TEXT,
  path         TEXT,
  status       INTEGER,
  model        TEXT,
  stream       INTEGER,
  is_sse       INTEGER,
  req_blob     TEXT,
  resp_blob    TEXT,
  truncated    INTEGER NOT NULL DEFAULT 0,
  in_tokens    INTEGER NOT NULL DEFAULT 0,
  out_tokens   INTEGER NOT NULL DEFAULT 0,
  cache_read   INTEGER NOT NULL DEFAULT 0,
  cache_write  INTEGER NOT NULL DEFAULT 0,
  est_cost     REAL NOT NULL DEFAULT 0,
  stop_reason  TEXT,
  num_messages INTEGER NOT NULL DEFAULT 0,
  num_tools    INTEGER NOT NULL DEFAULT 0,
  error        TEXT
);
CREATE INDEX IF NOT EXISTS idx_requests_session ON requests(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts_start);

CREATE TABLE IF NOT EXISTS content_blocks (
  id           INTEGER PRIMARY KEY,
  request_id   INTEGER NOT NULL REFERENCES requests(id),
  session_id   INTEGER NOT NULL,
  role         TEXT,
  idx          INTEGER,
  type         TEXT,
  tool_name    TEXT,
  tool_use_id  TEXT,
  is_error     INTEGER NOT NULL DEFAULT 0,
  char_len     INTEGER,
  text_preview TEXT
);
CREATE INDEX IF NOT EXISTS idx_blocks_request ON content_blocks(request_id);
CREATE INDEX IF NOT EXISTS idx_blocks_tool ON content_blocks(type, tool_name);

CREATE VIRTUAL TABLE IF NOT EXISTS blocks_fts USING fts5(
  text, block_id UNINDEXED, request_id UNINDEXED, session_id UNINDEXED
);
`

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

// BlockRow is one content block to persist for analytics, previews and search.
type BlockRow struct {
	Role        string
	Idx         int
	Type        string
	ToolName    string
	ToolUseID   string
	IsError     bool
	CharLen     int
	TextPreview string
}

// Record is a fully parsed exchange ready to persist. The ingest writer builds
// it; the store turns it into rows.
type Record struct {
	SessionKey string
	Model      string
	Cwd        string
	GitBranch  string
	CliVersion string

	StartedAt  time.Time
	EndedAt    time.Time
	DurationMs int64
	Method     string
	Path       string
	Status     int
	Stream     bool
	IsSSE      bool
	Truncated  bool

	ReqBody  []byte
	RespBody []byte

	In, Out, CacheRead, CacheWrite int
	EstCost                        float64
	StopReason                     string
	NumMessages                    int
	NumTools                       int
	Error                          string

	Blocks []BlockRow
}

// WriteExchange persists one request/response exchange and updates the rolling
// session aggregates, all in a single transaction. The returned ids let the
// caller (the live SSE stream) reference the new rows.
func (s *Store) WriteExchange(r *Record) (sessionID, requestID int64, seq int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	start := r.StartedAt.UnixMilli()
	end := r.EndedAt.UnixMilli()

	// Upsert the session and read back its id.
	if _, err = tx.Exec(`
		INSERT INTO sessions (session_key, first_seen, last_seen, model, cwd, git_branch, cli_version)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			last_seen   = excluded.last_seen,
			model       = COALESCE(NULLIF(excluded.model,''), sessions.model),
			cwd         = COALESCE(NULLIF(sessions.cwd,''), NULLIF(excluded.cwd,'')),
			git_branch  = COALESCE(NULLIF(sessions.git_branch,''), NULLIF(excluded.git_branch,'')),
			cli_version = COALESCE(NULLIF(sessions.cli_version,''), NULLIF(excluded.cli_version,''))
	`, r.SessionKey, start, end, r.Model, r.Cwd, r.GitBranch, r.CliVersion); err != nil {
		return
	}
	if err = tx.QueryRow(`SELECT id FROM sessions WHERE session_key = ?`, r.SessionKey).Scan(&sessionID); err != nil {
		return
	}

	var maxSeq sql.NullInt64
	if err = tx.QueryRow(`SELECT MAX(seq) FROM requests WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return
	}
	seq = int(maxSeq.Int64) + 1

	reqBlob, err := putBlob(tx, r.ReqBody)
	if err != nil {
		return
	}
	respBlob, err := putBlob(tx, r.RespBody)
	if err != nil {
		return
	}

	res, err := tx.Exec(`
		INSERT INTO requests (session_id, seq, ts_start, ts_end, duration_ms, method, path, status,
			model, stream, is_sse, req_blob, resp_blob, truncated, in_tokens, out_tokens,
			cache_read, cache_write, est_cost, stop_reason, num_messages, num_tools, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sessionID, seq, start, end, r.DurationMs, r.Method, r.Path, r.Status,
		r.Model, b2i(r.Stream), b2i(r.IsSSE), reqBlob, respBlob, b2i(r.Truncated),
		r.In, r.Out, r.CacheRead, r.CacheWrite, r.EstCost, r.StopReason,
		r.NumMessages, r.NumTools, r.Error)
	if err != nil {
		return
	}
	requestID, err = res.LastInsertId()
	if err != nil {
		return
	}

	for _, blk := range r.Blocks {
		var bres sql.Result
		bres, err = tx.Exec(`
			INSERT INTO content_blocks (request_id, session_id, role, idx, type, tool_name, tool_use_id, is_error, char_len, text_preview)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			requestID, sessionID, blk.Role, blk.Idx, blk.Type, blk.ToolName, blk.ToolUseID, b2i(blk.IsError), blk.CharLen, blk.TextPreview)
		if err != nil {
			return
		}
		if blk.TextPreview != "" {
			var blockID int64
			blockID, _ = bres.LastInsertId()
			if _, err = tx.Exec(`INSERT INTO blocks_fts (text, block_id, request_id, session_id) VALUES (?,?,?,?)`,
				blk.TextPreview, blockID, requestID, sessionID); err != nil {
				return
			}
		}
	}

	errInc := 0
	if r.Status >= 400 || r.Error != "" {
		errInc = 1
	}
	if _, err = tx.Exec(`
		UPDATE sessions SET
			num_requests = num_requests + 1,
			in_tokens    = in_tokens + ?,
			out_tokens   = out_tokens + ?,
			cache_read   = cache_read + ?,
			cache_write  = cache_write + ?,
			est_cost     = est_cost + ?,
			error_count  = error_count + ?
		WHERE id = ?`,
		r.In, r.Out, r.CacheRead, r.CacheWrite, r.EstCost, errInc, sessionID); err != nil {
		return
	}

	err = tx.Commit()
	return
}

// putBlob gzip-compresses body and stores it under its sha256, deduplicating
// identical bodies. It returns the hash (empty for an empty body).
func putBlob(tx *sql.Tx, body []byte) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM blobs WHERE hash = ?`, hash).Scan(&exists); err == nil {
		return hash, nil
	} else if err != sql.ErrNoRows {
		return "", err
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO blobs (hash, gz, raw_size) VALUES (?,?,?)`, hash, buf.Bytes(), len(body)); err != nil {
		return "", err
	}
	return hash, nil
}

// loadBlob returns the decompressed body for a hash, or nil when hash is empty.
func (s *Store) loadBlob(hash string) ([]byte, error) {
	if hash == "" {
		return nil, nil
	}
	var gz []byte
	if err := s.db.QueryRow(`SELECT gz FROM blobs WHERE hash = ?`, hash).Scan(&gz); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
