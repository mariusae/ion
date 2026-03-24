package exec

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ion/internal/core/cmdlang"
	"ion/internal/core/text"
)

func TestExecuteChangeDirectoryCommand(t *testing.T) {
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

func TestGroupedChangesApplyInParallel(t *testing.T) {
	t.Parallel()

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	if _, _, err := f.LoadInitial(bytes.NewReader([]byte("Emacs vi Emacs\n"))); err != nil {
		t.Fatalf("load initial: %v", err)
	}

	sess := NewSession(io.Discard)
	sess.Diag = io.Discard
	sess.AddFile(f)

	parser := cmdlang.NewParser(",x/Emacs|vi/{\ng/Emacs/ c/vi/\ng/vi/ c/Emacs/\n}\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ok {
		t.Fatal("execute requested stop")
	}

	var out bytes.Buffer
	if _, err := f.WriteTo(&out); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if got, want := out.String(), "vi Emacs vi\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

func TestQuotedFileAddressLoadsUnreadFileAndReportsStatus(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	aPath := filepath.Join(root, "a.txt")
	bPath := filepath.Join(root, "b.txt")
	if err := os.WriteFile(aPath, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(bPath, []byte("two\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	newNamedFile := func(name string) *text.File {
		t.Helper()
		d, err := text.NewDisk()
		if err != nil {
			t.Fatalf("new disk: %v", err)
		}
		t.Cleanup(func() {
			_ = d.Close()
		})
		f := text.NewFile(d)
		s := text.NewStringFromUTF8(name)
		if err := f.Name.DupString(&s); err != nil {
			t.Fatalf("set name: %v", err)
		}
		return f
	}

	fa := newNamedFile(aPath)
	if err := loadUnreadFile(fa); err != nil {
		t.Fatalf("load current file: %v", err)
	}
	fb := newNamedFile(bPath)

	var out bytes.Buffer
	var diag bytes.Buffer
	sess := NewSession(&out)
	sess.Diag = &diag
	sess.AddFile(fa)
	sess.AddFile(fb)
	sess.Current = fa

	parser := cmdlang.NewParser("\"b.txt\"p\nf\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ok {
		t.Fatal("execute requested stop")
	}
	cmd, err = parser.Parse()
	if err != nil {
		t.Fatalf("parse second command: %v", err)
	}
	ok, err = sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute second command: %v", err)
	}
	if !ok {
		t.Fatal("execute second command requested stop")
	}
	if sess.Current != fb {
		t.Fatalf("current file not switched to b.txt")
	}
	if fb.Unread {
		t.Fatalf("quoted-file target remained unread")
	}
	if got, want := diag.String(), " -  "+bPath+"\n -. "+bPath+"\n"; got != want {
		t.Fatalf("diag = %q, want %q", got, want)
	}
}

func TestSetCurrentAddressMovesDotWithoutPrinting(t *testing.T) {
	t.Parallel()

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	if _, _, err := f.LoadInitial(bytes.NewReader([]byte("one\nfunc here\ntwo\n"))); err != nil {
		t.Fatalf("load initial: %v", err)
	}

	var out bytes.Buffer
	sess := NewSession(&out)
	sess.Diag = io.Discard
	sess.AddFile(f)
	sess.Current = f

	if err := sess.SetCurrentAddress("/func"); err != nil {
		t.Fatalf("SetCurrentAddress() error = %v", err)
	}
	if got, want := out.String(), ""; got != want {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got, want := f.Dot, (text.Range{P1: 4, P2: 8}); got != want {
		t.Fatalf("dot = %#v, want %#v", got, want)
	}
}

func TestOpenFilesPathsTreatsColonSuffixAsLiteralPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "notes:2")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write literal colon file: %v", err)
	}

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag

	if err := sess.OpenFilesPaths([]string{path}); err != nil {
		t.Fatalf("OpenFilesPaths() error = %v", err)
	}
	if got, want := trimToken(sess.Current.Name.UTF8()), path; got != want {
		t.Fatalf("current name = %q, want %q", got, want)
	}
	if got, want := sess.Current.Dot, (text.Range{}); got != want {
		t.Fatalf("dot = %#v, want zero value", got)
	}
}

func TestOpenFilesPathsAtomicRestoresPreviousCurrentOnFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	missing := filepath.Join(root, "missing.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag

	if err := sess.OpenFilesPaths([]string{path}); err != nil {
		t.Fatalf("OpenFilesPaths() error = %v", err)
	}
	previous := sess.Current
	previousFiles := len(sess.Files)

	err := sess.OpenFilesPathsAtomic([]string{missing})
	if err == nil {
		t.Fatal("OpenFilesPathsAtomic() error = nil, want missing-file failure")
	}
	if got, want := trimToken(sess.Current.Name.UTF8()), path; got != want {
		t.Fatalf("current name = %q, want restored %q", got, want)
	}
	if sess.Current != previous {
		t.Fatal("current file pointer changed on failed atomic open")
	}
	if got, want := len(sess.Files), previousFiles; got != want {
		t.Fatalf("file count = %d, want %d", got, want)
	}
	if got := sess.findFileByName(missing); got != nil {
		t.Fatalf("missing file entry = %#v, want rolled back", got)
	}
	if got, err := sess.CurrentText(); err != nil || got != "alpha\nbeta\n" {
		t.Fatalf("CurrentText() = (%q, %v), want original text and no error", got, err)
	}
}

func TestFileCommandPrintsPendingNewName(t *testing.T) {
	t.Parallel()

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8("a.txt")
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}
	f.Unread = false

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag
	sess.AddFile(f)

	parser := cmdlang.NewParser("f renamed.txt\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ok {
		t.Fatal("execute requested stop")
	}
	if got, want := diag.String(), "'-. renamed.txt\n"; got != want {
		t.Fatalf("diag = %q, want %q", got, want)
	}
}

func TestWriteFailsWhileChangingInSameSequence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8(path)
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := loadUnreadFile(f); err != nil {
		t.Fatalf("load file: %v", err)
	}

	sess := NewSession(io.Discard)
	sess.Diag = io.Discard
	sess.AddFile(f)

	parser := cmdlang.NewParser("{\nf renamed.txt\nw\n}\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ok, err := sess.Execute(cmd)
	if ok {
		t.Fatal("execute ok = true, want false on write-while-changing error")
	}
	if err == nil {
		t.Fatal("execute error = nil, want write-while-changing failure")
	}
	if got, want := err.Error(), `can't write while changing: "`+path+`"`; got != want {
		t.Fatalf("execute error = %v, want %q", err, want)
	}
}

func TestWriteHonorsAddressRange(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	outPath := filepath.Join(root, "first.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8(path)
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := loadUnreadFile(f); err != nil {
		t.Fatalf("load file: %v", err)
	}
	f.Seq = 1
	f.CleanSeq = 0
	f.Mod = true

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag
	sess.Seq = 1
	sess.AddFile(f)

	parser := cmdlang.NewParser("1w " + outPath + "\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ok {
		t.Fatal("execute requested stop")
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out file: %v", err)
	}
	if got, want := string(data), "one\n"; got != want {
		t.Fatalf("written contents = %q, want %q", got, want)
	}
	if got, want := diag.String(), outPath+": #4\n"; got != want {
		t.Fatalf("diag = %q, want %q", got, want)
	}
	if !f.IsDirty() {
		t.Fatal("partial write unexpectedly marked file clean")
	}
}

func TestWriteWarnsForNewFileAndMissingFinalNewline(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8(path)
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := loadUnreadFile(f); err != nil {
		t.Fatalf("load file: %v", err)
	}

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag
	sess.AddFile(f)

	renamedPath := filepath.Join(root, "renamed.txt")
	parser := cmdlang.NewParser("f " + renamedPath + "\n")
	cmd, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse rename: %v", err)
	}
	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute rename: %v", err)
	}
	if !ok {
		t.Fatal("rename requested stop")
	}

	cmd, err = cmdlang.NewParser("w\n").Parse()
	if err != nil {
		t.Fatalf("parse write: %v", err)
	}
	ok, err = sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute write: %v", err)
	}
	if !ok {
		t.Fatal("write requested stop")
	}

	want := "'-. " + renamedPath + "\n" + renamedPath + ": (new file) ?warning: last char not newline\n#1\n"
	if got := diag.String(); got != want {
		t.Fatalf("diag = %q, want %q", got, want)
	}
	data, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatalf("read renamed.txt: %v", err)
	}
	if got, want := string(data), "x"; got != want {
		t.Fatalf("written contents = %q, want %q", got, want)
	}
}

func TestWriteWarnsWhenDiskFileChanged(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	name := text.NewStringFromUTF8(path)
	if err := f.Name.DupString(&name); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if err := loadUnreadFile(f); err != nil {
		t.Fatalf("load file: %v", err)
	}

	if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.txt: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes a.txt: %v", err)
	}

	var diag bytes.Buffer
	sess := NewSession(io.Discard)
	sess.Diag = &diag
	sess.AddFile(f)

	cmd, err := cmdlang.NewParser("w\n").Parse()
	if err != nil {
		t.Fatalf("parse write: %v", err)
	}
	ok, err := sess.Execute(cmd)
	if err != nil {
		t.Fatalf("execute write: %v", err)
	}
	if !ok {
		t.Fatal("write requested stop")
	}

	if got, want := diag.String(), "?warning: write might change good version of `"+path+"'\n"; got != want {
		t.Fatalf("diag = %q, want %q", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if got, want := string(data), "y\n"; got != want {
		t.Fatalf("disk contents = %q, want %q", got, want)
	}
}

func TestReplaceCurrentDeletesRange(t *testing.T) {
	t.Parallel()

	d, err := text.NewDisk()
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	t.Cleanup(func() {
		_ = d.Close()
	})

	f := text.NewFile(d)
	if _, _, err := f.LoadInitial(bytes.NewReader([]byte("alpha\n"))); err != nil {
		t.Fatalf("load initial: %v", err)
	}

	sess := NewSession(io.Discard)
	sess.Diag = io.Discard
	sess.AddFile(f)

	if err := sess.ReplaceCurrent(1, 2, ""); err != nil {
		t.Fatalf("ReplaceCurrent(delete) error = %v", err)
	}

	got, err := sess.CurrentText()
	if err != nil {
		t.Fatalf("CurrentText() error = %v", err)
	}
	if want := "apha\n"; got != want {
		t.Fatalf("CurrentText() = %q, want %q", got, want)
	}
	if got, want := sess.CurrentDot(), (text.Range{P1: 1, P2: 1}); got != want {
		t.Fatalf("CurrentDot() = %#v, want %#v", got, want)
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
