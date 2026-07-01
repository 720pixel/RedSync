package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// progress widgets. both fall back to plain one-line output when stderr is not a
// terminal, so piped logs and CI stay readable.

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func clearLine() { fmt.Fprint(os.Stderr, "\r\033[K") }

// Spinner animates a label with an elapsed-seconds counter for work we can't put
// a percentage on (stream copies, injects).
type Spinner struct {
	stop  chan struct{}
	wg    sync.WaitGroup
	tty   bool
	quiet bool
}

func StartSpinner(label string) *Spinner {
	if quiet {
		return &Spinner{quiet: true}
	}
	s := &Spinner{stop: make(chan struct{}), tty: stderrIsTTY()}
	if !s.tty {
		fmt.Fprintln(os.Stderr, tag(stepSty, "*")+" "+label)
		return s
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		start := time.Now()
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				frame := stepSty.Render(spinFrames[i%len(spinFrames)])
				secs := Muted.Render(fmt.Sprintf("%.0fs", time.Since(start).Seconds()))
				fmt.Fprintf(os.Stderr, "\r%s %s %s", frame, label, secs)
				i++
			}
		}
	}()
	return s
}

func (s *Spinner) Stop(done string) {
	if s.quiet {
		return
	}
	if s.tty {
		close(s.stop)
		s.wg.Wait()
		clearLine()
	}
	fmt.Fprintln(os.Stderr, tag(okStyle, "✓")+" "+done)
}

// Fail stops the spinner with an error glyph, so an aborted stage doesn't leave
// a misleading ✓ (or a live spinner) behind.
func (s *Spinner) Fail(msg string) {
	if s.quiet {
		return
	}
	if s.tty {
		close(s.stop)
		s.wg.Wait()
		clearLine()
	}
	fmt.Fprintln(os.Stderr, tag(errSty, "✗")+" "+msg)
}

// Bar is a determinate progress bar for work that reports a percentage (mux).
type Bar struct {
	label string
	width int
	tty   bool
	quiet bool
	last  int
}

func NewBar(label string) *Bar {
	if quiet {
		return &Bar{quiet: true, last: -1}
	}
	b := &Bar{label: label, width: 26, tty: stderrIsTTY(), last: -1}
	if !b.tty {
		fmt.Fprintln(os.Stderr, tag(stepSty, "*")+" "+label)
	}
	return b
}

func (b *Bar) Update(pct int) {
	if b.quiet || !b.tty || pct == b.last {
		return
	}
	b.last = pct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := b.width * pct / 100
	bar := barStyle.Render(strings.Repeat("█", filled)) + Muted.Render(strings.Repeat("░", b.width-filled))
	fmt.Fprintf(os.Stderr, "\r%s %s %s %s", stepSty.Render("▶"), b.label, bar, Title.Render(fmt.Sprintf("%3d%%", pct)))
}

func (b *Bar) Done(done string) {
	if b.quiet {
		return
	}
	if b.tty {
		clearLine()
	}
	fmt.Fprintln(os.Stderr, tag(okStyle, "✓")+" "+done)
}
