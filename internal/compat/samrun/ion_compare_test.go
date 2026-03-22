package samrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestIonMatchesCurrentCorpus(t *testing.T) {
	t.Parallel()

	moduleRoot := findModuleRoot(t)
	ionBin := buildIonBinary(t, moduleRoot)
	casesRoot := filepath.Join(moduleRoot, "testdata", "cases")

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

	oracle := Runner{}
	candidate := CommandRunner{
		Binary:   ionBin,
		BaseArgs: []string{"-d"},
	}

	for _, name := range caseNames {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tc, err := LoadCaseDir(filepath.Join(casesRoot, name))
			if err != nil {
				t.Fatalf("load case: %v", err)
			}

			workDir := filepath.Join(t.TempDir(), "work")
			oracleRes, err := oracle.RunInDir(context.Background(), workDir, tc)
			if err != nil {
				t.Fatalf("oracle run: %v", err)
			}
			candidateRes, err := candidate.RunInDir(context.Background(), workDir, tc)
			if err != nil {
				t.Fatalf("candidate run: %v", err)
			}
			mismatches := CompareResults(oracleRes, candidateRes)
			if len(mismatches) != 0 {
				t.Fatalf("mismatches: %#v\noracle stdout:\n%s\ncandidate stdout:\n%s\noracle stderr:\n%s\ncandidate stderr:\n%s",
					mismatches, oracleRes.Stdout, candidateRes.Stdout, oracleRes.Stderr, candidateRes.Stderr)
			}
		})
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
