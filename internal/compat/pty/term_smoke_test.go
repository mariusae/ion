package pty

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ion/internal/compat/samrun"
)

func TestSamDownloadTTYEnterExitBufferMode(t *testing.T) {
	t.Parallel()

	bin, err := samrun.FindBinary()
	if err != nil {
		t.Fatalf("find sam binary: %v", err)
	}

	workDir := t.TempDir()
	path := filepath.Join(workDir, "in.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-d", "in.txt")
	cmd.Dir = workDir

	sess, err := Start(ctx, cmd, 24, 80)
	if err != nil {
		t.Fatalf("start pty session: %v", err)
	}
	defer func() {
		_ = sess.Close()
	}()

	if _, err := sess.WaitFor(" -. in.txt", 2*time.Second); err != nil {
		if strings.Contains(sess.Snapshot(), "openpty: Operation not permitted") {
			t.Skip("PTY allocation is not permitted in this environment")
		}
		t.Fatalf("wait for startup output: %v\n%s", err, sess.Snapshot())
	}

	if err := sess.WriteString("\x1b"); err != nil {
		t.Fatalf("send ESC to enter buffer mode: %v", err)
	}
	if _, err := sess.WaitFor("\x1b[?1049h", 2*time.Second); err != nil {
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
