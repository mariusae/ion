package samrun

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIonMatchesFileWorkflowMatrix(t *testing.T) {
	t.Parallel()

	moduleRoot := findModuleRoot(t)
	ionBin := buildIonBinary(t, moduleRoot)

	oracle := Runner{}
	candidate := CommandRunner{
		Binary:   ionBin,
		BaseArgs: []string{"-d"},
	}

	cases := []struct {
		name string
		tc   Case
	}{
		{
			name: "rename_write_undo_name",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("f renamed.txt\nw\nu\nf\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("alpha\n")},
				CaptureFiles: []string{"in.txt", "renamed.txt"},
			},
		},
		{
			name: "edit_other_file_then_undo_restores_current",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("e other.txt\nu\n,p\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("one\ntwo\n"), "other.txt": []byte("alpha\nbeta\n")},
				CaptureFiles: []string{"in.txt", "other.txt"},
			},
		},
		{
			name: "read_then_undo_clears_dirty_quit",
			tc: Case{
				Args:         []string{},
				Script:       []byte("r in.txt\nu\nq\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("hello\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "addressed_write_then_report_name",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("1w part.txt\nf\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("one\ntwo\n")},
				CaptureFiles: []string{"in.txt", "part.txt"},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			compareIonCase(t, oracle, candidate, tc.tc)
		})
	}
}

func TestIonMatchesRegexpExecutionMatrix(t *testing.T) {
	t.Parallel()

	moduleRoot := findModuleRoot(t)
	ionBin := buildIonBinary(t, moduleRoot)

	oracle := Runner{}
	candidate := CommandRunner{
		Binary:   ionBin,
		BaseArgs: []string{"-d"},
	}

	type regexpCase struct {
		name    string
		content string
		script  string
	}

	cases := []regexpCase{
		{
			name:    "forward_search_class",
			content: "alpha\nbeta\ngamma\n",
			script:  "/[bg][a-z]+/p\nq\n",
		},
		{
			name:    "reverse_search_group",
			content: "alpha\nbeta\ngamma\n",
			script:  "0,$\n?(beta|alpha)?p\nq\n",
		},
		{
			name:    "global_print_group",
			content: "alpha\nbeta\ngamma\n",
			script:  ",g/(alpha|gamma)/p\nq\n",
		},
		{
			name:    "inverse_global_default_print",
			content: "alpha\nbeta\ngamma\n",
			script:  ",v/alpha/\nq\n",
		},
		{
			name:    "x_loop_alternation",
			content: "alpha beta gamma\n",
			script:  ",x/(alpha|beta)/p\nq\n",
		},
		{
			name:    "y_loop_complement_class",
			content: "alpha beta gamma\n",
			script:  ",y/[ab]+/p\nq\n",
		},
		{
			name:    "substitute_global_class",
			content: "abracadabra\n",
			script:  ",s/[ab]/X/g\n,p\nq\n",
		},
		{
			name:    "substitute_group_ampersand",
			content: "alpha beta alpha\n",
			script:  ",s/(alpha)/<&>/g\n,p\nq\n",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			compareIonCase(t, oracle, candidate, Case{
				Args:         []string{"in.txt"},
				Script:       []byte(tc.script),
				Files:        map[string][]byte{"in.txt": []byte(tc.content)},
				CaptureFiles: []string{"in.txt"},
			})
		})
	}
}

func TestIonMatchesCommandAddressMatrix(t *testing.T) {
	t.Parallel()

	moduleRoot := findModuleRoot(t)
	ionBin := buildIonBinary(t, moduleRoot)

	oracle := Runner{}
	candidate := CommandRunner{
		Binary:   ionBin,
		BaseArgs: []string{"-d"},
	}

	cases := []struct {
		name string
		tc   Case
	}{
		{
			name: "insert_before_line",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("2i\ntwo\n.\n,p\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("one\nthree\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "default_v_print_from_address",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("2v/alpha/\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("alpha\nbeta\ngamma\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "undo_then_redo_print",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("2c\nTWO\n.\nu\nu-1\n,p\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("one\ntwo\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "char_range_print",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("#6,#10p\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("alpha\nbeta\ngamma\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "reverse_search_print",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte("?beta?p\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("alpha\nbeta\ngamma\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
		{
			name: "group_block_print_and_position",
			tc: Case{
				Args:         []string{"in.txt"},
				Script:       []byte(",g/a/{\n=\np\n}\nq\n"),
				Files:        map[string][]byte{"in.txt": []byte("alpha\nbeta\ngamma\n")},
				CaptureFiles: []string{"in.txt"},
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			compareIonCase(t, oracle, candidate, tc.tc)
		})
	}
}

func compareIonCase(t *testing.T, oracle Runner, candidate CommandRunner, tc Case) {
	t.Helper()

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
}
