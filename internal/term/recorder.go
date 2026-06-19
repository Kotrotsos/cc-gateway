package term

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// recorder writes a plain-text, ANSI-free transcript of full requests and
// responses to a timestamped file. Pressing "f" opens a fresh file; pressing
// "f" again closes it. It is safe for concurrent in-flight requests.
type recorder struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func (rec *recorder) on() bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.file != nil
}

// toggle starts a new recording file or stops the current one, returning a
// status message describing the new state and whether recording is now on.
func (rec *recorder) toggle() (msg string, nowOn bool, ok bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.file != nil {
		path := rec.path
		rec.file.Close()
		rec.file = nil
		rec.path = ""
		return "recording OFF  →  " + path, false, true
	}

	path := fmt.Sprintf("cc-gateway-%s.log", time.Now().Format("20060102-150405"))
	f, err := os.Create(path)
	if err != nil {
		return "recording failed: " + err.Error(), false, false
	}
	rec.file = f
	rec.path = path
	return "recording ON  →  " + path + "  (raw JSON request/response bodies)", true, true
}

// write appends one full request or response record to the file, if recording.
func (rec *recorder) write(header string, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.file == nil {
		return
	}
	const bar = "════════════════════════════════════════════════════════════════════"
	fmt.Fprintf(rec.file, "%s\n%s  %s\n%s\n", bar, header, time.Now().Format("2006-01-02 15:04:05.000"),
		"────────────────────────────────────────────────────────────────────")
	rec.file.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		rec.file.WriteString("\n")
	}
	rec.file.WriteString("\n")
}
