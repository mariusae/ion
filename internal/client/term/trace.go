package term

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type traceLogger struct {
	mu  sync.Mutex
	f   *os.File
	seq atomic.Uint64
}

func newTraceLogger() *traceLogger {
	path := strings.TrimSpace(os.Getenv("ION_TERM_TRACE"))
	if path == "" {
		return nil
	}
	if !strings.Contains(path, "/") {
		path = "/tmp/ion-term-trace.log"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	logger := &traceLogger{f: f}
	logger.Printf("trace-start pid=%d", os.Getpid())
	return logger
}

func (l *traceLogger) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.Printf("trace-stop")
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *traceLogger) Printf(format string, args ...any) {
	if l == nil || l.f == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	seq := l.seq.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.f, "%s #%d %s\n", time.Now().Format(time.RFC3339Nano), seq, line)
}
