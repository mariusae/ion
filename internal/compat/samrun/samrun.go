package samrun

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Case describes one sam -d execution in an isolated temporary workspace.
type Case struct {
	Args         []string
	Script       []byte
	Files        map[string][]byte
	CaptureFiles []string
	Env          []string
}

// Result captures process outputs and selected file contents after execution.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Files     map[string][]byte
	WorkDir   string
	Binary    string
	InvokedAs []string
}

// Runner executes sam -d as the compatibility oracle.
type Runner struct {
	Binary string
}

// CommandRunner executes an arbitrary command against a Case using the same
// workspace layout as Runner.
type CommandRunner struct {
	Binary   string
	BaseArgs []string
}

// Comparison holds raw outputs from an oracle run and a candidate run.
type Comparison struct {
	Oracle    Result
	Candidate Result
}

// Mismatch describes one byte-for-byte divergence between two runs.
type Mismatch struct {
	Field string
	Note  string
}

// FindBinary resolves the sam binary path.
func FindBinary() (string, error) {
	if p := strings.TrimSpace(os.Getenv("ION_SAM_BIN")); p != "" {
		return p, nil
	}
	return exec.LookPath("sam")
}

// LoadCaseDir loads a case from disk.
//
// Layout:
//   - script.sam: stdin sent to sam -d
//   - args.txt: optional, one argv entry per line
//   - files/: optional fixture tree copied into the temp workdir
//   - capture.txt: optional, one relative file path per line to capture after run
func LoadCaseDir(dir string) (Case, error) {
	script, err := os.ReadFile(filepath.Join(dir, "script.sam"))
	if err != nil {
		return Case{}, fmt.Errorf("read script.sam: %w", err)
	}

	tc := Case{
		Script: script,
		Files:  make(map[string][]byte),
	}

	if args, err := readOptionalLines(filepath.Join(dir, "args.txt")); err != nil {
		return Case{}, err
	} else if len(args) > 0 {
		tc.Args = args
	}

	filesDir := filepath.Join(dir, "files")
	if info, err := os.Stat(filesDir); err == nil && info.IsDir() {
		err = filepath.WalkDir(filesDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(filesDir, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tc.Files[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return Case{}, fmt.Errorf("load files dir: %w", err)
		}
	}

	capture, err := readOptionalLines(filepath.Join(dir, "capture.txt"))
	if err != nil {
		return Case{}, err
	}
	if len(capture) > 0 {
		tc.CaptureFiles = capture
	} else {
		for name := range tc.Files {
			tc.CaptureFiles = append(tc.CaptureFiles, name)
		}
		sort.Strings(tc.CaptureFiles)
	}

	return tc, nil
}

// Run executes one case. Non-zero sam exit status is reported in Result.ExitCode
// and does not cause an error.
func (r Runner) Run(ctx context.Context, tc Case) (Result, error) {
	bin := r.Binary
	if bin == "" {
		var err error
		bin, err = FindBinary()
		if err != nil {
			return Result{}, fmt.Errorf("find sam binary: %w", err)
		}
	}

	workDir, err := os.MkdirTemp("", "ion-samrun-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}

	return r.RunInDir(ctx, workDir, tc)
}

// Run executes one case for an arbitrary binary.
func (r CommandRunner) Run(ctx context.Context, tc Case) (Result, error) {
	workDir, err := os.MkdirTemp("", "ion-cmdrun-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	return r.RunInDir(ctx, workDir, tc)
}

// RunInDir executes one case in the provided workdir.
func (r Runner) RunInDir(ctx context.Context, workDir string, tc Case) (Result, error) {
	bin := r.Binary
	if bin == "" {
		var err error
		bin, err = FindBinary()
		if err != nil {
			return Result{}, fmt.Errorf("find sam binary: %w", err)
		}
	}

	if err := os.RemoveAll(workDir); err != nil {
		return Result{}, fmt.Errorf("reset workdir %q: %w", workDir, err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("mkdir workdir %q: %w", workDir, err)
	}

	for name, data := range tc.Files {
		path, err := safeJoin(workDir, name)
		if err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{}, fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return Result{}, fmt.Errorf("write fixture %q: %w", name, err)
		}
	}

	argv := append([]string{"-d"}, tc.Args...)
	return runPreparedCommand(ctx, bin, argv, workDir, tc)
}

// RunInDir executes one case in the provided workdir for an arbitrary command.
func (r CommandRunner) RunInDir(ctx context.Context, workDir string, tc Case) (Result, error) {
	bin := r.Binary
	if bin == "" {
		return Result{}, fmt.Errorf("command runner binary is required")
	}

	if err := os.RemoveAll(workDir); err != nil {
		return Result{}, fmt.Errorf("reset workdir %q: %w", workDir, err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("mkdir workdir %q: %w", workDir, err)
	}

	for name, data := range tc.Files {
		path, err := safeJoin(workDir, name)
		if err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{}, fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return Result{}, fmt.Errorf("write fixture %q: %w", name, err)
		}
	}

	argv := append(append([]string(nil), r.BaseArgs...), tc.Args...)
	return runPreparedCommand(ctx, bin, argv, workDir, tc)
}

func runPreparedCommand(ctx context.Context, bin string, argv []string, workDir string, tc Case) (Result, error) {
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), tc.Env...)
	cmd.Stdin = bytes.NewReader(tc.Script)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !asExitError(err, &exitErr) {
			return Result{}, fmt.Errorf("run sam: %w", err)
		}
		exitCode = exitErr.ExitCode()
	}

	files := make(map[string][]byte, len(tc.CaptureFiles))
	for _, name := range tc.CaptureFiles {
		path, err := safeJoin(workDir, name)
		if err != nil {
			return Result{}, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Result{}, fmt.Errorf("read captured file %q: %w", name, err)
		}
		files[name] = data
	}

	return Result{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		ExitCode:  exitCode,
		Files:     files,
		WorkDir:   workDir,
		Binary:    bin,
		InvokedAs: append([]string{bin}, argv...),
	}, nil
}

// RunDifferential executes oracle and candidate in the same absolute workdir so
// path-sensitive output stays directly comparable.
func RunDifferential(ctx context.Context, oracle, candidate Runner, tc Case) (Comparison, []Mismatch, error) {
	workDir, err := os.MkdirTemp("", "ion-diff-*")
	if err != nil {
		return Comparison{}, nil, fmt.Errorf("create differential workdir: %w", err)
	}

	oracleResult, err := oracle.RunInDir(ctx, workDir, tc)
	if err != nil {
		return Comparison{}, nil, fmt.Errorf("oracle run: %w", err)
	}

	candidateResult, err := candidate.RunInDir(ctx, workDir, tc)
	if err != nil {
		return Comparison{}, nil, fmt.Errorf("candidate run: %w", err)
	}

	comparison := Comparison{
		Oracle:    oracleResult,
		Candidate: candidateResult,
	}
	return comparison, CompareResults(oracleResult, candidateResult), nil
}

// CompareResults reports byte-for-byte mismatches in observable behavior.
func CompareResults(oracle, candidate Result) []Mismatch {
	var mismatches []Mismatch

	if !bytes.Equal(oracle.Stdout, candidate.Stdout) {
		mismatches = append(mismatches, Mismatch{Field: "stdout", Note: "stdout differs"})
	}
	if !bytes.Equal(oracle.Stderr, candidate.Stderr) {
		mismatches = append(mismatches, Mismatch{Field: "stderr", Note: "stderr differs"})
	}
	if oracle.ExitCode != candidate.ExitCode {
		mismatches = append(mismatches, Mismatch{
			Field: "exit_code",
			Note:  fmt.Sprintf("oracle=%d candidate=%d", oracle.ExitCode, candidate.ExitCode),
		})
	}

	for _, name := range unionFileKeys(oracle.Files, candidate.Files) {
		oracleData, oracleOK := oracle.Files[name]
		candidateData, candidateOK := candidate.Files[name]
		switch {
		case !oracleOK:
			mismatches = append(mismatches, Mismatch{
				Field: "file:" + name,
				Note:  "file missing from oracle result",
			})
		case !candidateOK:
			mismatches = append(mismatches, Mismatch{
				Field: "file:" + name,
				Note:  "file missing from candidate result",
			})
		case !bytes.Equal(oracleData, candidateData):
			mismatches = append(mismatches, Mismatch{
				Field: "file:" + name,
				Note:  "file contents differ",
			})
		}
	}

	return mismatches
}

func readOptionalLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths are not allowed: %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." {
		return "", fmt.Errorf("empty relative path is not allowed")
	}
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("parent traversal is not allowed: %q", rel)
	}
	return filepath.Join(root, clean), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = exitErr
	return true
}

func unionFileKeys(a, b map[string][]byte) []string {
	keys := make(map[string]struct{}, len(a)+len(b))
	for name := range a {
		keys[name] = struct{}{}
	}
	for name := range b {
		keys[name] = struct{}{}
	}

	out := make([]string, 0, len(keys))
	for name := range keys {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
