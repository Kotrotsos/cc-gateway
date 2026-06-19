package term

import (
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// StartKeyboard puts the terminal into cbreak mode so single keypresses are read
// without Enter, and launches the key loop. It returns false (and changes
// nothing) when stdin is not an interactive terminal. Ctrl-C still works: signal
// generation stays enabled and the terminal is restored in a signal handler.
func (con *Console) StartKeyboard() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	st, err := stty("-g")
	if err != nil {
		return false
	}
	con.origStty = strings.TrimSpace(st)
	if _, err := stty("-icanon", "-echo", "min", "1", "time", "0"); err != nil {
		return false
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		con.restoreStty()
		os.Exit(0)
	}()

	go con.keyLoop()
	return true
}

func (con *Console) keyLoop() {
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case 's', 'S':
			now := !con.full.Load()
			con.full.Store(now)
			if now {
				con.statusLine(cGreen, "screen: showing FORMATTED messages  (press s to stop)")
			} else {
				con.statusLine(cYellow, "screen: formatted messages OFF")
			}
		case 'f', 'F':
			msg, nowOn, ok := con.rec.toggle()
			col := cYellow
			if ok && nowOn {
				col = cGreen
			} else if !ok {
				col = cRed
			}
			con.statusLine(col, msg)
		}
	}
}

func stty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	return string(out), err
}

func (con *Console) restoreStty() {
	if con.origStty != "" {
		stty(con.origStty)
	}
}
