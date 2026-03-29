package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunDownloadProcessesCommandsIncrementally(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stdinR, stdinW := io.Pipe()
	var stdout syncBuffer
	var stderr syncBuffer
	done := make(chan error, 1)

	go func() {
		done <- runDownload(config{download: true, files: []string{path}}, stdinR, &stdout, &stderr)
	}()

	waitFor(t, func() bool {
		return strings.Contains(stderr.String(), " -. "+path+"\n")
	}, "initial file status")

	if _, err := io.WriteString(stdinW, ",\n"); err != nil {
		t.Fatalf("WriteString(first command) error = %v", err)
	}

	waitFor(t, func() bool {
		return strings.Contains(stdout.String(), "alpha\nbeta\n")
	}, "command output before EOF")

	if _, err := io.WriteString(stdinW, "q\n"); err != nil {
		t.Fatalf("WriteString(quit) error = %v", err)
	}
	if err := stdinW.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDownload() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runDownload() did not return")
	}
}

func TestRunTermRejectsNonTTY(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "README.md")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stdinR, stdinW := io.Pipe()
	var stdout syncBuffer
	var stderr syncBuffer
	done := make(chan int, 1)

	go func() {
		done <- run([]string{path}, stdinR, &stdout, &stderr)
	}()
	if err := stdinW.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("run() exit code = %d, want 1", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not return")
	}

	if got := stderr.String(); !strings.Contains(got, "terminal mode requires a tty; use ion -d for command mode") {
		t.Fatalf("stderr = %q, want non-tty terminal-mode error", got)
	}
}

func TestRunHelpWritesUsage(t *testing.T) {
	t.Parallel()

	var stdout syncBuffer
	var stderr syncBuffer

	if got := run([]string{"-help"}, strings.NewReader(""), &stdout, &stderr); got != 0 {
		t.Fatalf("run(-help) = %d, want 0", got)
	}
	if got := stdout.String(); !strings.Contains(got, "usage: ion") || !strings.Contains(got, "ion <fully-qualified-command>") {
		t.Fatalf("stdout = %q, want usage text", got)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func waitFor(t *testing.T, cond func() bool, desc string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}
