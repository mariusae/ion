package main

import "testing"

func TestResidentAttachKeyUsesTmuxSessionWhenAvailable(t *testing.T) {
	t.Parallel()

	tmux := &fakeTmux{sessionID: "$7"}
	key, err := residentAttachKey(residentRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		tempDir:    t.TempDir,
		tmux:       tmux.run,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "tmux-session:$7"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentAttachKeyFallsBackToWorkingDirectory(t *testing.T) {
	t.Parallel()

	key, err := residentAttachKey(residentRuntime{
		getenv:     func(string) string { return "" },
		getwd:      func() (string, error) { return "/tmp/work/dir", nil },
		tempDir:    t.TempDir,
		tmux:       runTmuxCommand,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentAttachKey() error = %v", err)
	}
	if got, want := key, "cwd:/tmp/work/dir"; got != want {
		t.Fatalf("residentAttachKey() = %q, want %q", got, want)
	}
}

func TestResidentPathsUseSharedPrefix(t *testing.T) {
	t.Parallel()

	paths, err := residentPathsForRuntime(residentRuntime{
		getenv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux.sock"
			}
			return ""
		},
		getwd:      func() (string, error) { return "/tmp/work", nil },
		tempDir:    func() string { return "/tmp" },
		tmux:       (&fakeTmux{sessionID: "$9"}).run,
		executable: func() (string, error) { return "/tmp/bin/ion", nil },
	})
	if err != nil {
		t.Fatalf("residentPathsForRuntime() error = %v", err)
	}
	if got, want := paths.socketPath, "/tmp/ion/"+hashedPathBase(residentPathVersionPrefix, "tmux-session:$9")+".sock"; got != want {
		t.Fatalf("socketPath = %q, want %q", got, want)
	}
	if got, want := paths.panePath, "/tmp/ion/"+hashedPathBase(residentPathVersionPrefix, "tmux-session:$9")+".pane"; got != want {
		t.Fatalf("panePath = %q, want %q", got, want)
	}
}
