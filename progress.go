package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	jobRunning int32 = iota
	jobDone
	jobFail
)

type Job struct {
	label string
	cur   atomic.Int64
	total atomic.Int64
	state atomic.Int32
	msg   atomic.Pointer[string]
}

func (j *Job) Add(n int64) { j.cur.Add(n) }

func (j *Job) SetTotal(n int64) { j.total.Store(n) }

func (j *Job) Done() {
	if j.state.Load() == jobRunning {
		j.cur.Store(j.total.Load())
		j.state.Store(jobDone)
	}
}

func (j *Job) Fail(err error) {
	s := err.Error()
	j.msg.Store(&s)
	j.state.Store(jobFail)
}

type Progress struct {
	mu      sync.Mutex
	jobs    []*Job
	out     *os.File
	tty     bool
	stop    chan struct{}
	done    chan struct{}
	frames  int
	stopped bool
}

func NewProgress(out *os.File) *Progress {
	p := &Progress{
		out:  out,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	if fi, err := out.Stat(); err == nil {
		p.tty = (fi.Mode() & os.ModeCharDevice) != 0
	}
	return p
}

func (p *Progress) Add(label string) *Job {
	j := &Job{label: label}
	p.mu.Lock()
	p.jobs = append(p.jobs, j)
	p.mu.Unlock()
	return j
}

func (p *Progress) Start() {
	if !p.tty {
		return
	}
	go p.loop()
}

func (p *Progress) Stop() {
	if p.stopped {
		return
	}
	p.stopped = true
	if !p.tty {
		p.mu.Lock()
		for _, j := range p.jobs {
			fmt.Fprintln(p.out, formatLine(j))
		}
		p.mu.Unlock()
		return
	}
	close(p.stop)
	<-p.done
}

func (p *Progress) HasFailures() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, j := range p.jobs {
		if j.state.Load() == jobFail {
			return true
		}
	}
	return false
}

func (p *Progress) loop() {
	defer close(p.done)
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for {
		p.render()
		select {
		case <-p.stop:
			p.render()
			return
		case <-t.C:
		}
	}
}

func (p *Progress) render() {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	if p.frames > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", p.frames)
	}
	for _, j := range p.jobs {
		b.WriteString("\r\x1b[2K")
		b.WriteString(formatLine(j))
		b.WriteByte('\n')
	}
	p.frames = len(p.jobs)
	io.WriteString(p.out, b.String())
}

const (
	labelW = 32
	barW   = 30
)

func formatLine(j *Job) string {
	label := truncLabel(j.label, labelW)
	state := j.state.Load()
	cur := j.cur.Load()
	total := j.total.Load()

	var bar, status string
	switch state {
	case jobDone:
		bar = strings.Repeat("=", barW-1) + ">"
		status = "done"
	case jobFail:
		bar = strings.Repeat("-", barW)
		msg := ""
		if mp := j.msg.Load(); mp != nil {
			msg = *mp
		}
		status = "FAIL: " + truncLabel(msg, 40)
	default:
		pct := 0.0
		if total > 0 {
			pct = float64(cur) / float64(total)
			if pct > 1 {
				pct = 1
			}
		}
		filled := int(float64(barW) * pct)
		if filled > 0 && filled < barW {
			bar = strings.Repeat("=", filled-1) + ">" + strings.Repeat("-", barW-filled)
		} else {
			bar = strings.Repeat("=", filled) + strings.Repeat("-", barW-filled)
		}
		if total > 0 {
			status = fmt.Sprintf("%s/%s %3.0f%%", humanBytes(cur), humanBytes(total), pct*100)
		} else {
			status = "scanning..."
		}
	}
	return fmt.Sprintf("%-*s [%s] %s", labelW, label, bar, status)
}

func truncLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return "…" + s[len(s)-(n-1):]
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u; v /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
