package samrun

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestRunCase_PrintFile(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	caseDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "testdata", "cases", "print-file")

	tc, err := LoadCaseDir(caseDir)
	if err != nil {
		t.Fatalf("load case: %v", err)
	}

	runner := Runner{}
	res, err := runner.Run(context.Background(), tc)
	if err != nil {
		t.Fatalf("run case: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(res.WorkDir); err != nil {
			t.Fatalf("remove temp dir: %v", err)
		}
	})
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code %d\nstdout:\n%s\nstderr:\n%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if got := res.Files["in.txt"]; !bytes.Equal(got, []byte("hello\nworld\n")) {
		t.Fatalf("fixture file changed unexpectedly: %q", got)
	}
	if !bytes.Contains(res.Stdout, []byte("hello\nworld\n")) {
		t.Fatalf("stdout did not contain file contents:\n%s", res.Stdout)
	}
}

func TestRunDifferential_SameBinaryMatches(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	caseDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "testdata", "cases", "print-file")

	tc, err := LoadCaseDir(caseDir)
	if err != nil {
		t.Fatalf("load case: %v", err)
	}

	runner := Runner{}
	comparison, mismatches, err := RunDifferential(context.Background(), runner, runner, tc)
	if err != nil {
		t.Fatalf("run differential case: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(comparison.Oracle.WorkDir); err != nil {
			t.Fatalf("remove temp dir: %v", err)
		}
	})

	if len(mismatches) != 0 {
		t.Fatalf("expected identical results, got mismatches: %#v", mismatches)
	}
}

func TestCorpusCases_RunAndSelfCompare(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	casesRoot := filepath.Join(filepath.Dir(filename), "..", "..", "..", "testdata", "cases")

	entries, err := os.ReadDir(casesRoot)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}

	var caseNames []string
	for _, entry := range entries {
		if entry.IsDir() {
			caseNames = append(caseNames, entry.Name())
		}
	}
	sort.Strings(caseNames)

	for _, name := range caseNames {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tc, err := LoadCaseDir(filepath.Join(casesRoot, name))
			if err != nil {
				t.Fatalf("load case: %v", err)
			}

			runner := Runner{}
			res, err := runner.Run(context.Background(), tc)
			if err != nil {
				t.Fatalf("run case: %v", err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(res.WorkDir); err != nil {
					t.Fatalf("remove temp dir: %v", err)
				}
			})
			if res.ExitCode != 0 {
				t.Fatalf("unexpected exit code %d\nstdout:\n%s\nstderr:\n%s", res.ExitCode, res.Stdout, res.Stderr)
			}

			comparison, mismatches, err := RunDifferential(context.Background(), runner, runner, tc)
			if err != nil {
				t.Fatalf("run differential case: %v", err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(comparison.Oracle.WorkDir); err != nil {
					t.Fatalf("remove temp dir: %v", err)
				}
			})
			if len(mismatches) != 0 {
				t.Fatalf("expected identical results, got mismatches: %#v", mismatches)
			}
		})
	}
}
