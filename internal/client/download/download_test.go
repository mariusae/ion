package download

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serversession "ion/internal/server/session"
	"ion/internal/server/workspace"
)

func TestRunEOFWarnsOnDirtyFiles(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	svc := serversession.NewDownload(workspace.New(), &stdout, &stderr)

	if err := Run([]string{path}, strings.NewReader("a\nbeta\n.\n"), &stderr, svc); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "?changed files\n") {
		t.Fatalf("stderr = %q, want changed-files warning", got)
	}
}

func TestRunEOFIgnoresTrailingIncompleteCommand(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	svc := serversession.NewDownload(workspace.New(), &stdout, &stderr)

	if err := Run([]string{path}, strings.NewReader("a\nbeta\n.\ns/a/\\"), &stderr, svc); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stderr.String()
	if strings.Contains(got, "?bad rhs\n") || strings.Contains(got, "?newline expected\n") {
		t.Fatalf("stderr = %q, want incomplete trailing command ignored", got)
	}
	if !strings.Contains(got, "?changed files\n") {
		t.Fatalf("stderr = %q, want changed-files warning", got)
	}
}

func TestRunReportsBareUnknownCommandToken(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	svc := serversession.NewDownload(workspace.New(), &stdout, &stderr)

	if err := Run([]string{path}, strings.NewReader("xyz\nq\n"), &stderr, svc); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "?unknown command `xyz'\n") {
		t.Fatalf("stderr = %q, want bare unknown command diagnostic", got)
	}
}
