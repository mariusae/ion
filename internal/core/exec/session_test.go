package exec

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"ion/internal/core/cmdlang"
	"ion/internal/core/text"
)

func TestExecuteChangeDirectoryCommand(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write one.txt: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir root: %v", err)
	}

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8("one.txt")
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag
	sess.AddFile(f)

	parser := cmdlang.NewParser("cd sub\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cmd == nil {
		t.Fatal("parse returned nil command")
	}
	if cmd.Cmdc != 'c'|0x100 {
		t.Fatalf("cmdc = %q (%U), want cd sentinel", cmd.Cmdc, cmd.Cmdc)
	}

	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ok {
		t.Fatal("execute requested stop")
	}

	subdir := filepath.Join(root, "sub")
	if got, err := os.Getwd(); err != nil {
		t.Fatalf("getwd after cd: %v", err)
	} else if !sameFilePath(t, got, subdir) {
		t.Fatalf("cwd = %q, want same directory as %q", got, subdir)
	}

	if got := diag.String(); got != "!\n" {
		t.Fatalf("diag = %q, want %q", got, "!\n")
	}

	gotName := trimToken(f.Name.UTF8())
	if !filepath.IsAbs(gotName) {
		t.Fatalf("rewritten file name = %q, want absolute path", gotName)
	}
	if !sameFilePath(t, gotName, filepath.Join(root, "one.txt")) {
		t.Fatalf("rewritten file name = %q, want same file as %q", gotName, filepath.Join(root, "one.txt"))
	}
}

func sameFilePath(t *testing.T, got, want string) bool {
	t.Helper()

	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %q: %v", got, err)
	}
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat %q: %v", want, err)
	}
	return os.SameFile(gotInfo, wantInfo)
}
