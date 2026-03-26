package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"ion/internal/proto/wire"
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

	var stdout2 bytes.Buffer
	var stderr2 bytes.Buffer
	run := NewDownload(ws, &stdout2, &stderr2)
	if _, err := run.Execute(",p\n"); err != nil {
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

func TestTermSessionNavigationCommandsTrackFileHistory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	goMod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(readme, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}
	if err := os.WriteFile(goMod, []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{readme}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("B " + goMod + "\n"); err != nil {
		t.Fatalf("Execute(B) error = %v", err)
	}
	stderr.Reset()
	view, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after B error = %v", err)
	}
	if got, want := view.Name, goMod; got != want {
		t.Fatalf("current file after B = %q, want %q", got, want)
	}

	if _, err := sess.Execute("P\n"); err != nil {
		t.Fatalf("Execute(P) error = %v", err)
	}
	if got, want := stderr.String(), " -. "+readme+"\n"; got != want {
		t.Fatalf("stderr after P = %q, want %q", got, want)
	}
	stderr.Reset()
	view, err = sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after P error = %v", err)
	}
	if got, want := view.Name, readme; got != want {
		t.Fatalf("current file after P = %q, want %q", got, want)
	}

	if _, err := sess.Execute("N\n"); err != nil {
		t.Fatalf("Execute(N) error = %v", err)
	}
	if got, want := stderr.String(), " -. "+goMod+"\n"; got != want {
		t.Fatalf("stderr after N = %q, want %q", got, want)
	}
	view, err = sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after N error = %v", err)
	}
	if got, want := view.Name, goMod; got != want {
		t.Fatalf("current file after N = %q, want %q", got, want)
	}
}

func TestTermSessionNavigationCommandsClearForwardHistory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws := workspace.New()
	sess := NewTerm(ws, nil, nil)
	if err := sess.Bootstrap([]string{path}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	start, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() start error = %v", err)
	}

	line2, err := sess.SetAddress("2")
	if err != nil {
		t.Fatalf("SetAddress(2) error = %v", err)
	}
	if sameBufferView(start, line2) {
		t.Fatal("SetAddress(2) did not change the current view")
	}

	if _, err := sess.Execute("P\n"); err != nil {
		t.Fatalf("Execute(P) error = %v", err)
	}
	restored, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() restored error = %v", err)
	}
	if !sameBufferView(start, restored) {
		t.Fatalf("restored view = %#v, want %#v", restored, start)
	}

	line3, err := sess.SetAddress("3")
	if err != nil {
		t.Fatalf("SetAddress(3) error = %v", err)
	}
	if _, err := sess.Execute("N\n"); err != nil {
		t.Fatalf("Execute(N) error = %v", err)
	}
	afterForward, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after N error = %v", err)
	}
	if !sameBufferView(line3, afterForward) {
		t.Fatalf("view after cleared-forward N = %#v, want %#v", afterForward, line3)
	}
}

func TestTermSessionNavigationCommandsDoNotPrintStatusWithinSameFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{path}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.SetAddress("2"); err != nil {
		t.Fatalf("SetAddress(2) error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("P\n"); err != nil {
		t.Fatalf("Execute(P) error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr after same-file P = %q, want empty", got)
	}
}

func TestTermSessionShowNavigationStack(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	goMod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(readme, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}
	if err := os.WriteFile(goMod, []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{readme}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.SetAddress("2"); err != nil {
		t.Fatalf("SetAddress(2) error = %v", err)
	}
	if _, err := sess.Execute("B " + goMod + "\n"); err != nil {
		t.Fatalf("Execute(B) error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}

	want := "" +
		"-  " + readme + ":#0\n" +
		"-  " + readme + ":#6,#11\n" +
		"*  " + goMod + ":#0\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after S = %q, want %q", got, want)
	}
}

func sameBufferView(a, b wire.BufferView) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.DotStart == b.DotStart &&
		a.DotEnd == b.DotEnd
}
