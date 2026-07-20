package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	conversationstenography "conversationstenography"
)

type terminalProgress struct {
	out       io.Writer
	label     string
	width     int
	mu        sync.Mutex
	lastPct   int
	lastDraw  time.Time
	enabled   bool
	started   bool
}

func newTerminalProgress(out io.Writer, label string) *terminalProgress {
	enabled := false
	if f, ok := out.(*os.File); ok {
		enabled = term.IsTerminal(int(f.Fd()))
	}
	return &terminalProgress{out: out, label: label, width: 28, enabled: enabled, lastPct: -1}
}

func (p *terminalProgress) Update(done, total int) {
	if p == nil || !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = true
	pct := 0
	if total > 0 {
		pct = done * 100 / total
		if pct > 100 {
			pct = 100
		}
	}
	now := time.Now()
	if pct == p.lastPct && now.Sub(p.lastDraw) < 50*time.Millisecond && (total == 0 || done < total) {
		return
	}
	p.lastPct = pct
	p.lastDraw = now
	if total <= 0 {
		fmt.Fprintf(p.out, "\r  %s… %d", p.label, done)
		return
	}
	filled := pct * p.width / 100
	if filled > p.width {
		filled = p.width
	}
	bar := strings.Repeat("#", filled) + strings.Repeat(".", p.width-filled)
	fmt.Fprintf(p.out, "\r  %s [%s] %3d%%", p.label, bar, pct)
}

func (p *terminalProgress) Finish() {
	if p == nil || !p.enabled || !p.started {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.out, "\r\033[K")
	p.started = false
	p.lastPct = -1
}

func withChainProgress(chain *conversationstenography.ConversationChain, out io.Writer, label string) func() {
	progress := newTerminalProgress(out, label)
	chain.SetProgressCallback(progress.Update)
	return func() {
		chain.SetProgressCallback(nil)
		progress.Finish()
	}
}
