package main

import (
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// logBuf keeps the last lines of the log for the web page.
type logBuf struct {
	mu    sync.Mutex
	lines []string
}

var recent = &logBuf{}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		l.lines = append(l.lines, s)
	}
	if len(l.lines) > 200 {
		l.lines = l.lines[len(l.lines)-200:]
	}
	return len(p), nil
}

func (l *logBuf) Last(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) < n {
		n = len(l.lines)
	}
	out := make([]string, n)
	for i := range out { // newest first
		out[i] = l.lines[len(l.lines)-1-i]
	}
	return out
}

func init() { log.SetOutput(io.MultiWriter(os.Stderr, recent)) }
