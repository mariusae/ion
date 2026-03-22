package pty

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIonTermEnterExitBufferMode(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[?1049h", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for alternate-screen enter: %v\n%s", err, sess.Snapshot())
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		t.Fatalf("wait for file contents in buffer mode: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to exit buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[?1049l", 2*time.Second); err != nil {
		t.Fatalf("wait for alternate-screen exit: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("q\n"); err != nil {
		t.Fatalf("send quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermCommandModeBackspace(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if err := sess.WriteString(",px\x7f\n"); err != nil {
		t.Fatalf("send edited print command: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for print output: %v\n%s", err, sess.Snapshot())
	}
	if strings.Contains(sess.Snapshot(), "?unknown command") {
		t.Fatalf("command-mode backspace did not remove trailing input:\n%s", sess.Snapshot())
	}

	if err := sess.WriteString("q\n"); err != nil {
		t.Fatalf("send quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModePageDown(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	var text strings.Builder
	for i := 1; i <= 60; i++ {
		text.WriteString(fmt.Sprintf("line%03d\n", i))
	}
	if err := os.WriteFile(path, []byte(text.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("line001", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b[B"); err != nil {
		t.Fatalf("send page-down arrow: %v", err)
	}
	if _, err := sess.WaitFor("line025", 2*time.Second); err != nil {
		t.Fatalf("wait for paged buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to exit buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[?1049l", 2*time.Second); err != nil {
		t.Fatalf("wait for alternate-screen exit: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("q\n"); err != nil {
		t.Fatalf("send quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeCtrlFMovesCursor(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[7ma\x1b[27m", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial cursor highlight: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x06"); err != nil {
		t.Fatalf("send Ctrl-F: %v", err)
	}
	if _, err := sess.WaitFor("a\x1b[7ml\x1b[27mpha", 2*time.Second); err != nil {
		t.Fatalf("wait for moved cursor highlight: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1bq\n"); err != nil {
		t.Fatalf("exit buffer mode and quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeInsertPersistsToCommandMode(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[7ma\x1b[27m", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial cursor highlight: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("Z\x1b,p\n"); err != nil {
		t.Fatalf("insert Z, exit buffer mode, and print: %v", err)
	}
	if _, err := sess.WaitFor("Zalpha", 2*time.Second); err != nil {
		t.Fatalf("wait for printed edited contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("q\n"); err != nil {
		t.Fatalf("send quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "..")
}

func buildIonBinary(t *testing.T, moduleRoot string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ion")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ion")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ion binary: %v\n%s", err, out)
	}
	return bin
}
