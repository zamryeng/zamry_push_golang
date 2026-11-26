package poller

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// failingQueue fails every Claim, to prove the loop keeps going.
type failingQueue struct {
	push.JobStore
	calls atomic.Int32
}

func (f *failingQueue) Claim(context.Context, time.Time, int) ([]push.Job, error) {
	f.calls.Add(1)
	return nil, context.DeadlineExceeded
}

// logBuffer collects log output from the poller's own goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
