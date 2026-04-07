package session

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clienttarget "ion/internal/client/target"
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

func TestDownloadSessionReportsUnknownNamespacedCommand(t *testing.T) {
	t.Parallel()

	ws := workspace.New()
	sess := NewDownload(ws, io.Discard, io.Discard)
	if _, err := sess.Execute(":client\n"); err == nil || err.Error() != "unknown command `:client'" {
		t.Fatalf("Execute(:client) error = %v, want %q", err, "unknown command `:client'")
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

	if _, err := sess.PushTarget([]string{goMod}); err != nil {
		t.Fatalf("PushTarget(go.mod) error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr after PushTarget = %q, want empty", got)
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
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr after P = %q, want empty", got)
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
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr after N = %q, want empty", got)
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

	line2, err := sess.PushTarget([]string{path + ":2"})
	if err != nil {
		t.Fatalf("PushTarget(file:2) error = %v", err)
	}
	if sameBufferView(start, line2) {
		t.Fatal("PushTarget(file:2) did not change the current view")
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

	line3, err := sess.PushTarget([]string{path + ":3"})
	if err != nil {
		t.Fatalf("PushTarget(file:3) error = %v", err)
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

	if _, err := sess.PushTarget([]string{path + ":2"}); err != nil {
		t.Fatalf("PushTarget(file:2) error = %v", err)
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

	if _, err := sess.PushTarget([]string{readme + ":2"}); err != nil {
		t.Fatalf("PushTarget(readme:2) error = %v", err)
	}
	if _, err := sess.PushTarget([]string{goMod}); err != nil {
		t.Fatalf("PushTarget(go.mod) error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}

	want := "" +
		" -  " + readme + ":#0\n" +
		" -  " + readme + ":#6,#11\n" +
		" -. " + goMod + ":#0\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after S = %q, want %q", got, want)
	}
}

func TestTermSessionShowNavigationStackMarksDirtyCurrentEntry(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(readme, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{readme}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.PushTarget([]string{readme + ":2"}); err != nil {
		t.Fatalf("PushTarget(readme:2) error = %v", err)
	}
	view, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if _, err := sess.Replace(view.DotStart, view.DotStart, "x"); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}

	want := "" +
		" -  " + readme + ":#0\n" +
		"'-. " + readme + ":#6,#11\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr after S = %q, want %q", got, want)
	}
}

func TestTermSessionDescribeDemoSymbol(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caller := filepath.Join(root, "a.go")
	target := filepath.Join(root, "b.go")
	if err := os.WriteFile(caller, []byte("package main\n\nfunc call() {\n\tFoo()\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.go) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("package main\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.go) error = %v", err)
	}

	ws := workspace.New()
	var stdout bytes.Buffer
	sess := NewTerm(ws, &stdout, io.Discard)
	if err := sess.Bootstrap([]string{caller, target}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := sess.SetAddress("/Foo/"); err != nil {
		t.Fatalf("SetAddress(/Foo/) error = %v", err)
	}
	stdout.Reset()

	if _, err := sess.Execute(":demo:describe\n"); err != nil {
		t.Fatalf("Execute(:demo:describe) error = %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "symbol Foo "+caller+":4:2") {
		t.Fatalf("stdout = %q, want symbol description for call site", got)
	}
}

func TestTermSessionGotoDemoSymbol(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caller := filepath.Join(root, "a.go")
	target := filepath.Join(root, "b.go")
	if err := os.WriteFile(caller, []byte("package main\n\nfunc call() {\n\tFoo()\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.go) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("package main\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.go) error = %v", err)
	}

	ws := workspace.New()
	var stdout bytes.Buffer
	sess := NewTerm(ws, &stdout, io.Discard)
	if err := sess.Bootstrap([]string{caller, target}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := sess.SetAddress("/Foo/"); err != nil {
		t.Fatalf("SetAddress(/Foo/) error = %v", err)
	}
	stdout.Reset()

	if _, err := sess.Execute(":demo:goto\n"); err != nil {
		t.Fatalf("Execute(:demo:goto) error = %v", err)
	}

	view, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() error = %v", err)
	}
	if got, want := view.Name, target; got != want {
		t.Fatalf("view.Name = %q, want %q", got, want)
	}
	selected := string([]rune(view.Text)[view.DotStart:view.DotEnd])
	if got, want := selected, "Foo"; got != want {
		t.Fatalf("selected symbol = %q, want %q", got, want)
	}
	if got := stdout.String(); !strings.Contains(got, "goto Foo "+target+":3:6") {
		t.Fatalf("stdout = %q, want goto target line", got)
	}
}

func TestTargetOpenWithAddressDoesNotRecordNavigation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fileA := filepath.Join(root, "a.txt")
	fileB := filepath.Join(root, "b.txt")
	if err := os.WriteFile(fileA, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt) error = %v", err)
	}
	if err := os.WriteFile(fileB, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{fileA}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	// Simulate right-click-to-B on a token like "b.txt:2".
	if _, err := clienttarget.Open(sess, []string{fileB + ":2"}); err != nil {
		t.Fatalf("target.Open() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("nav stack = %q, want empty", got)
	}
}

func TestTargetOpenWithoutAddressDoesNotRecordNavigation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fileA := filepath.Join(root, "a.txt")
	fileB := filepath.Join(root, "b.txt")
	if err := os.WriteFile(fileA, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt) error = %v", err)
	}
	if err := os.WriteFile(fileB, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{fileA}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if _, err := clienttarget.Open(sess, []string{fileB}); err != nil {
		t.Fatalf("target.Open() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("nav stack = %q, want empty", got)
	}
}

func TestExecuteAddressedBDoesNotRecordNavigation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fileA := filepath.Join(root, "a.txt")
	fileB := filepath.Join(root, "b.txt")
	if err := os.WriteFile(fileA, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.txt) error = %v", err)
	}
	if err := os.WriteFile(fileB, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.txt) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{fileA}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := sess.Execute("B " + fileB + ":2\n"); err != nil {
		t.Fatalf("Execute(B file:2) error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("nav stack = %q, want empty", got)
	}
}

func TestExecuteAddressedBOnCurrentFileDoesNotOpenNamelessBuffer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	sess := NewTerm(ws, nil, io.Discard)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	view, err := sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() before Execute error = %v", err)
	}
	if view.Name != file {
		t.Fatalf("current file before Execute = %q, want %q", view.Name, file)
	}

	if _, err := sess.Execute("B " + file + ":2\n"); err != nil {
		t.Fatalf("Execute(B current-file:2) error = %v", err)
	}

	view, err = sess.CurrentView()
	if err != nil {
		t.Fatalf("CurrentView() after Execute error = %v", err)
	}
	if view.Name != file {
		t.Fatalf("current file after Execute = %q, want %q", view.Name, file)
	}
	if got, want := view.DotStart, 4; got != want {
		t.Fatalf("DotStart = %d, want %d", got, want)
	}
	if got, want := view.DotEnd, 8; got != want {
		t.Fatalf("DotEnd = %d, want %d", got, want)
	}
}

func TestOpenTargetOnCurrentFileDoesNotOpenNamelessBuffer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	sess := NewTerm(ws, nil, io.Discard)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	view, err := sess.OpenTarget(file, "2")
	if err != nil {
		t.Fatalf("OpenTarget(current-file, 2) error = %v", err)
	}
	if view.Name != file {
		t.Fatalf("current file after OpenTarget = %q, want %q", view.Name, file)
	}
	if got, want := view.DotStart, 4; got != want {
		t.Fatalf("DotStart = %d, want %d", got, want)
	}
	if got, want := view.DotEnd, 8; got != want {
		t.Fatalf("DotEnd = %d, want %d", got, want)
	}
}

func TestOpenTargetWithCharacterAddressUsesExactPoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	sess := NewTerm(ws, nil, io.Discard)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	view, err := sess.OpenTarget(file, "#5")
	if err != nil {
		t.Fatalf("OpenTarget(current-file, #5) error = %v", err)
	}
	if view.Name != file {
		t.Fatalf("current file after OpenTarget = %q, want %q", view.Name, file)
	}
	if got, want := view.DotStart, 5; got != want {
		t.Fatalf("DotStart = %d, want %d", got, want)
	}
	if got, want := view.DotEnd, 5; got != want {
		t.Fatalf("DotEnd = %d, want %d", got, want)
	}
}

func TestOpenTargetWithLegacyLineColumnUsesLineStartOffset(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	sess := NewTerm(ws, nil, io.Discard)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	view, err := sess.OpenTarget(file, "2-0+#2")
	if err != nil {
		t.Fatalf("OpenTarget(current-file, 2-0+#2) error = %v", err)
	}
	if view.Name != file {
		t.Fatalf("current file after OpenTarget = %q, want %q", view.Name, file)
	}
	if got, want := view.DotStart, 6; got != want {
		t.Fatalf("DotStart = %d, want %d", got, want)
	}
	if got, want := view.DotEnd, 6; got != want {
		t.Fatalf("DotEnd = %d, want %d", got, want)
	}
}

func TestPushTargetClearsForwardHistoryFromCurrentPoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.PushTarget([]string{file + ":2"}); err != nil {
		t.Fatalf("PushTarget(file:2) error = %v", err)
	}
	if _, err := sess.PushTarget([]string{file + ":3"}); err != nil {
		t.Fatalf("PushTarget(file:3) error = %v", err)
	}
	if _, err := sess.Execute("P\n"); err != nil {
		t.Fatalf("Execute(P) error = %v", err)
	}
	if _, err := sess.PushTarget([]string{file + ":1"}); err != nil {
		t.Fatalf("PushTarget(file:1) error = %v", err)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}
	want := "" +
		" -  " + file + ":#0\n" +
		" -  " + file + ":#4,#8\n" +
		" -. " + file + ":#0,#4\n"
	if got := stderr.String(); got != want {
		t.Fatalf("nav stack =\n%s\nwant:\n%s", got, want)
	}
}

func TestPopNavigationRemovesCurrentEntry(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(README.md) error = %v", err)
	}

	ws := workspace.New()
	var stderr bytes.Buffer
	sess := NewTerm(ws, nil, &stderr)
	if err := sess.Bootstrap([]string{file}); err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	if _, err := sess.PushTarget([]string{file + ":2"}); err != nil {
		t.Fatalf("PushTarget(file:2) error = %v", err)
	}
	if _, err := sess.PushTarget([]string{file + ":3"}); err != nil {
		t.Fatalf("PushTarget(file:3) error = %v", err)
	}

	view, err := sess.PopNavigation()
	if err != nil {
		t.Fatalf("PopNavigation() error = %v", err)
	}
	if got, want := view.DotStart, 4; got != want {
		t.Fatalf("DotStart after pop = %d, want %d", got, want)
	}
	if got, want := view.DotEnd, 8; got != want {
		t.Fatalf("DotEnd after pop = %d, want %d", got, want)
	}
	stderr.Reset()

	if _, err := sess.Execute("S\n"); err != nil {
		t.Fatalf("Execute(S) error = %v", err)
	}
	want := "" +
		" -  " + file + ":#0\n" +
		" -. " + file + ":#4,#8\n"
	if got := stderr.String(); got != want {
		t.Fatalf("nav stack =\n%s\nwant:\n%s", got, want)
	}
}

func sameBufferView(a, b wire.BufferView) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.DotStart == b.DotStart &&
		a.DotEnd == b.DotEnd
}
