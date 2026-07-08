package term

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestScanRecursiveFilePickerRespectsIgnoreFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".gitignore"), "/ion\n*.profraw\nignored-dir/\n")
	mustWriteFile(t, filepath.Join(root, "keep.go"), "package main\n")
	mustWriteFile(t, filepath.Join(root, "ion"), "binary\n")
	mustWriteFile(t, filepath.Join(root, "default.profraw"), "profile\n")
	mustWriteFile(t, filepath.Join(root, "ignored-dir", "hidden.go"), "package hidden\n")
	mustWriteFile(t, filepath.Join(root, "nested", ".gitignore"), "*.tmp\n")
	mustWriteFile(t, filepath.Join(root, "nested", "keep.txt"), "keep\n")
	mustWriteFile(t, filepath.Join(root, "nested", "drop.tmp"), "drop\n")

	updates := make(chan recursivePickerUpdate, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go scanRecursiveFilePicker(ctx, 7, root, updates, io.Discard)

	var got []string
	timeout := time.After(2 * time.Second)
	for {
		select {
		case update := <-updates:
			if update.generation != 7 {
				t.Fatalf("generation = %d, want 7", update.generation)
			}
			if update.err != nil {
				t.Fatalf("scan error = %v", update.err)
			}
			got = append(got, update.paths...)
			if update.done {
				rel := make([]string, 0, len(got))
				for _, path := range got {
					r, err := filepath.Rel(root, path)
					if err != nil {
						t.Fatalf("Rel(%q) error = %v", path, err)
					}
					rel = append(rel, filepath.ToSlash(r))
				}
				sort.Strings(rel)
				want := []string{".gitignore", "keep.go", "nested/.gitignore", "nested/keep.txt"}
				if !reflect.DeepEqual(rel, want) {
					t.Fatalf("scanned files = %#v, want %#v", rel, want)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for scan")
		}
	}
}

func TestRecursiveIgnoreMatcherSupportsNegation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".gitignore"), "*.log\n!important.log\n")

	matcher := newRecursiveIgnoreMatcher(root)
	matcher.loadDir(root)
	if !matcher.ignored(filepath.Join(root, "debug.log"), false) {
		t.Fatal("debug.log ignored = false, want true")
	}
	if matcher.ignored(filepath.Join(root, "important.log"), false) {
		t.Fatal("important.log ignored = true, want false")
	}
}

func TestRecursiveIgnoreMatcherSupportsHgIgnoreSyntax(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".hgignore"), "syntax: regexp\n^generated/.*\\.go$\nsyntax: glob\n*.tmp\n")

	matcher := newRecursiveIgnoreMatcher(root)
	matcher.loadDir(root)
	if !matcher.ignored(filepath.Join(root, "generated", "out.go"), false) {
		t.Fatal("generated/out.go ignored = false, want true")
	}
	if !matcher.ignored(filepath.Join(root, "scratch.tmp"), false) {
		t.Fatal("scratch.tmp ignored = false, want true")
	}
	if matcher.ignored(filepath.Join(root, "generated", "out.txt"), false) {
		t.Fatal("generated/out.txt ignored = true, want false")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
