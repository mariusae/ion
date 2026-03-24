package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRagePrintsDiagnosticsWithoutTTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("TMUX", "")

	var stdin bytes.Buffer
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := run([]string{"-rage"}, &stdin, &stdout, &stderr); code != 0 {
		t.Fatalf("run() exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{
		"ion terminal diagnostics",
		"color_mode: ansi256",
		"query_status: skipped",
		"query_reason: stdin is not an *os.File tty handle",
		"theme_enabled: false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout missing %q in %q", want, got)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestParseArgsRejectsRageWithFiles(t *testing.T) {
	t.Parallel()

	if _, err := parseArgs([]string{"-rage", "alpha"}); err == nil || !strings.Contains(err.Error(), "-rage does not take file arguments") {
		t.Fatalf("parseArgs() error = %v, want file-argument rejection", err)
	}
}
