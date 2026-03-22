package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string
	Imports    []string
}

func TestArchitectureBoundaries(t *testing.T) {
	t.Parallel()

	pkgs := goListPackages(t)

	assertNoForbiddenImports(t, pkgs, "ion/internal/core/", []string{
		"ion/internal/server/",
		"ion/internal/client/",
		"ion/cmd/",
	})
	assertNoForbiddenImports(t, pkgs, "ion/internal/server/", []string{
		"ion/internal/client/",
	})
	assertNoForbiddenImports(t, pkgs, "ion/internal/client/", []string{
		"ion/internal/server/",
	})
}

func goListPackages(t *testing.T) map[string]goListPackage {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filename)

	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("go list failed: %s", string(exitErr.Stderr))
		}
		t.Fatalf("go list failed: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	pkgs := make(map[string]goListPackage)
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		if pkg.ImportPath == "" {
			continue
		}
		pkgs[pkg.ImportPath] = pkg
	}
	return pkgs
}

func assertNoForbiddenImports(t *testing.T, pkgs map[string]goListPackage, pkgPrefix string, forbidden []string) {
	t.Helper()

	for _, pkg := range pkgs {
		if !strings.HasPrefix(pkg.ImportPath, pkgPrefix) {
			continue
		}
		for _, imp := range pkg.Imports {
			for _, bad := range forbidden {
				if strings.HasPrefix(imp, bad) {
					t.Fatalf("%s must not import %s (imports %s)", pkg.ImportPath, bad, imp)
				}
			}
		}
	}
}
