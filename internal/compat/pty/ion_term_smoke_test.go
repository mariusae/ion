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

func TestIonTermBufferModeCtrlQQuits(t *testing.T) {
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
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x11"); err != nil {
		t.Fatalf("send Ctrl-Q: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for Ctrl-Q exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeCtrlWSaves(t *testing.T) {
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

	if err := sess.WriteString("\x1bZ\x17"); err != nil {
		t.Fatalf("enter buffer mode, edit, and save: %v", err)
	}
	if _, err := sess.WaitFor("in.txt: #11", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for save status: %v\n%s", err, sess.Snapshot())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(data), "Zalpha\nbeta\n"; got != want {
		t.Fatalf("saved file = %q, want %q", got, want)
	}

	if err := sess.WriteString("\x11"); err != nil {
		t.Fatalf("send Ctrl-Q: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeCtrlLFindsNextSelection(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha\n"), 0o644); err != nil {
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

	if err := sess.WriteString("/alpha/\n"); err != nil {
		t.Fatalf("select first alpha in command mode: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for command-mode selection output: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b\x0c"); err != nil {
		t.Fatalf("enter buffer mode and look forward: %v", err)
	}
	if _, err := sess.WaitFor("alpha beta \x1b[7ma\x1b[27m", 2*time.Second); err != nil {
		t.Fatalf("wait for moved selection/cursor: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x11"); err != nil {
		t.Fatalf("send Ctrl-Q: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeMetaBackspaceDeletesWord(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha beta\n"), 0o644); err != nil {
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

	if err := sess.WriteString("\x1b\x06\x06\x06\x06\x06\x06\x06\x06\x06\x06\x1b\x7f"); err != nil {
		t.Fatalf("enter buffer mode, move to line end, and meta-backspace: %v", err)
	}
	if _, err := sess.WaitFor("alpha \x1b[7m \x1b[27m", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for deleted-word view: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x11"); err != nil {
		t.Fatalf("send Ctrl-Q: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeSnarfAndPaste(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha\n"), 0o644); err != nil {
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

	if err := sess.WriteString("/alpha/\n"); err != nil {
		t.Fatalf("select alpha in command mode: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial selection output: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b\x1bw\x05\x19"); err != nil {
		t.Fatalf("enter buffer mode, snarf, move to end, and paste: %v", err)
	}
	if _, err := sess.WaitFor("alpha beta alpha\x1b[7ma\x1b[27mlpha", 2*time.Second); err != nil {
		t.Fatalf("wait for pasted text in buffer mode: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x11"); err != nil {
		t.Fatalf("send Ctrl-Q: %v", err)
	}
	// Modified buffers require double q in command mode; save instead of quitting directly.
	if _, err := sess.WaitFor("?changed files", 2*time.Second); err != nil {
		t.Fatalf("wait for changed-files warning after Ctrl-Q: %v\n%s", err, sess.Snapshot())
	}
	if err := sess.WriteString("q\n"); err != nil {
		t.Fatalf("send second quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeOverlayRecall(t *testing.T) {
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
		t.Fatalf("enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\n,p\r"); err != nil {
		t.Fatalf("open overlay and run print command: %v", err)
	}
	if _, err := sess.WaitFor("> ,p", 2*time.Second); err != nil {
		t.Fatalf("wait for overlay command history: %v\n%s", err, sess.Snapshot())
	}
	if _, err := sess.WaitFor("beta", 2*time.Second); err != nil {
		t.Fatalf("wait for overlay command output: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x10"); err != nil {
		t.Fatalf("recall last overlay command: %v", err)
	}
	if _, err := sess.WaitFor(": ,p\x1b[7m \x1b[27m", 2*time.Second); err != nil {
		t.Fatalf("wait for recalled overlay prompt: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\n\x1f"); err != nil {
		t.Fatalf("close overlay and reopen with slash preload: %v", err)
	}
	if _, err := sess.WaitFor(": /\x1b[7m \x1b[27m", 2*time.Second); err != nil {
		t.Fatalf("wait for slash-preloaded overlay prompt: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b\x1bq\n"); err != nil {
		t.Fatalf("close overlay, exit buffer mode, and quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeCtrlSpaceSelectionSyncsBackToCommands(t *testing.T) {
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
		t.Fatalf("enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x00\x06\x06"); err != nil {
		t.Fatalf("toggle mark and move twice: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[7ma\x1b[27m\x1b[7ml\x1b[27mpha", 2*time.Second); err != nil {
		t.Fatalf("wait for active selection highlight: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1bp\nq\n"); err != nil {
		t.Fatalf("exit buffer mode, print selection, and quit: %v", err)
	}
	if _, err := sess.WaitFor("al", 2*time.Second); err != nil {
		t.Fatalf("wait for command-mode print of synced selection: %v\n%s", err, sess.Snapshot())
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeMouseSelectionSyncsBackToCommands(t *testing.T) {
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
		t.Fatalf("enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[?1006h", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for mouse enablement: %v\n%s", err, sess.Snapshot())
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b[<0;1;1M\x1b[<32;3;1M\x1b[<0;3;1m"); err != nil {
		t.Fatalf("send mouse drag selection: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[7ma\x1b[27m\x1b[7ml\x1b[27mpha", 2*time.Second); err != nil {
		t.Fatalf("wait for mouse-selected highlight: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1bp\nq\n"); err != nil {
		t.Fatalf("exit buffer mode, print selection, and quit: %v", err)
	}
	if _, err := sess.WaitFor("al", 2*time.Second); err != nil {
		t.Fatalf("wait for command-mode print of mouse selection: %v\n%s", err, sess.Snapshot())
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeMouseScroll(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "darwin" {
		t.Skip("terminal mode smoke test currently only supports darwin")
	}

	moduleRoot := findModuleRoot(t)
	bin := buildIonBinary(t, moduleRoot)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	var text strings.Builder
	for i := 1; i <= 40; i++ {
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
		t.Fatalf("enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("line001", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b[<65;1;1M"); err != nil {
		t.Fatalf("send mouse wheel down: %v", err)
	}
	if _, err := sess.WaitFor("line004", 2*time.Second); err != nil {
		t.Fatalf("wait for scrolled buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1bq\n"); err != nil {
		t.Fatalf("exit buffer mode and quit: %v", err)
	}
	if err := sess.WaitExit(2 * time.Second); err != nil {
		t.Fatalf("wait for exit: %v\n%s", err, sess.Snapshot())
	}
}

func TestIonTermBufferModeContextMenuOpenAndDismiss(t *testing.T) {
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
		t.Fatalf("enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("alpha", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for initial buffer contents: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b[<2;20;10M"); err != nil {
		t.Fatalf("open context menu: %v", err)
	}
	if _, err := sess.WaitFor("| write", 2*time.Second); err != nil {
		t.Fatalf("wait for context menu contents: %v\n%s", err, sess.Snapshot())
	}
	if _, err := sess.WaitFor("| /regexp", 2*time.Second); err != nil {
		t.Fatalf("wait for regexp menu item: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b\x1bq\n"); err != nil {
		t.Fatalf("dismiss menu, exit buffer mode, and quit: %v", err)
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
