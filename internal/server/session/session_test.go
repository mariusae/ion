package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"ion/internal/core/cmdlang"
	"ion/internal/server/workspace"
)

func TestDownloadSessionBindsClientStreams(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws := workspace.New()

	var stdout1 bytes.Buffer
	var stderr1 bytes.Buffer
	boot := NewDownload(ws, &stdout1, &stderr1)
	if err := boot.Bootstrap([]string{path}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if stdout1.Len() != 0 {
		t.Fatalf("bootstrap stdout = %q, want empty", stdout1.String())
	}
	if stderr1.Len() == 0 {
		t.Fatal("bootstrap stderr empty, want status output")
	}

	parser := cmdlang.NewParser(",p\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	var stdout2 bytes.Buffer
	var stderr2 bytes.Buffer
	run := NewDownload(ws, &stdout2, &stderr2)
	if _, err := run.Execute(cmd); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := stdout2.String(), "alpha\n"; got != want {
		t.Fatalf("session stdout = %q, want %q", got, want)
	}
	if stderr2.Len() != 0 {
		t.Fatalf("session stderr = %q, want empty", stderr2.String())
	}
	if stdout1.Len() != 0 {
		t.Fatalf("bootstrap stdout changed to %q, want empty", stdout1.String())
	}
}

func TestSessionIDsAreDistinct(t *testing.T) {
	t.Parallel()

	ws := workspace.New()
	a := NewDownload(ws, nil, nil)
	b := NewTerm(ws, nil, nil)
	if a.ID() == 0 || b.ID() == 0 {
		t.Fatalf("session IDs must be non-zero: a=%d b=%d", a.ID(), b.ID())
	}
	if a.ID() == b.ID() {
		t.Fatalf("session IDs must differ: a=%d b=%d", a.ID(), b.ID())
	}
}
