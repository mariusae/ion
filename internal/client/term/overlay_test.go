package term

import (
	"bytes"
	"testing"
)

func TestOverlayRecallRestoresSavedInput(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.open("draft")
	overlay.addCommand(",p")
	overlay.addCommand("w")

	if ok := overlay.recallPrev(); !ok {
		t.Fatalf("recallPrev() = false, want true")
	}
	if got, want := string(overlay.input), "w"; got != want {
		t.Fatalf("first recall = %q, want %q", got, want)
	}
	if ok := overlay.recallPrev(); !ok {
		t.Fatalf("second recallPrev() = false, want true")
	}
	if got, want := string(overlay.input), ",p"; got != want {
		t.Fatalf("second recall = %q, want %q", got, want)
	}
	if ok := overlay.recallNext(); !ok {
		t.Fatalf("recallNext() = false, want true")
	}
	if got, want := string(overlay.input), "w"; got != want {
		t.Fatalf("recallNext() = %q, want %q", got, want)
	}
	if ok := overlay.recallNext(); !ok {
		t.Fatalf("restore saved input = false, want true")
	}
	if got, want := string(overlay.input), "draft"; got != want {
		t.Fatalf("restored input = %q, want %q", got, want)
	}
}

func TestOutputCaptureFlushesBufferedLines(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)
	var lines []string

	capture.Start(func(line string) {
		lines = append(lines, line)
	})
	if _, err := capture.Stdout().Write([]byte("alpha\nbeta")); err != nil {
		t.Fatalf("stdout write: %v", err)
	}
	if _, err := capture.Stderr().Write([]byte("\ngamma\n")); err != nil {
		t.Fatalf("stderr write: %v", err)
	}
	capture.Stop()

	if got, want := len(lines), 3; got != want {
		t.Fatalf("captured lines = %d, want %d", got, want)
	}
	if got, want := lines[0], "alpha"; got != want {
		t.Fatalf("line 0 = %q, want %q", got, want)
	}
	if got, want := lines[1], "beta"; got != want {
		t.Fatalf("line 1 = %q, want %q", got, want)
	}
	if got, want := lines[2], "gamma"; got != want {
		t.Fatalf("line 2 = %q, want %q", got, want)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("captured output leaked to passthrough writers: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestOverlayHeightTracksHistoryWithinBounds(t *testing.T) {
	prev := termRows
	termRows = 20
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	if got, want := overlayHeight(overlay), 0; got != want {
		t.Fatalf("overlayHeight(hidden) = %d, want %d", got, want)
	}

	overlay.visible = true
	if got, want := overlayHeight(overlay), minOverlayRows; got != want {
		t.Fatalf("overlayHeight(empty) = %d, want %d", got, want)
	}

	for i := 0; i < 20; i++ {
		overlay.addOutput("line")
	}
	if got, want := overlayHeight(overlay), termRows/2; got != want {
		t.Fatalf("overlayHeight(full) = %d, want %d", got, want)
	}
}
