package term

import (
	"bytes"
	"testing"
	"time"
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

func TestOverlayReopenPreservesDraftAndCursor(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.open("draft")
	overlay.moveLeft()
	overlay.moveLeft()
	overlay.close()

	overlay.reopen()

	if !overlay.visible {
		t.Fatal("overlay.visible = false, want true after reopen")
	}
	if got, want := string(overlay.input), "draft"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := overlay.cursor, 3; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestOverlayOpenPrefillReplacesDraft(t *testing.T) {
	t.Parallel()

	overlay := newOverlayState()
	overlay.open("draft")
	overlay.close()

	overlay.open("/")

	if got, want := string(overlay.input), "/"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := overlay.cursor, 1; got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
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

func TestOutputCaptureStopFlushesTailWithoutDeadlock(t *testing.T) {
	t.Parallel()

	capture := NewOutputCapture(nil, nil)
	lines := make(chan string, 1)

	capture.Start(func(line string) {
		lines <- line
	})
	if _, err := capture.Stdout().Write([]byte("tail")); err != nil {
		t.Fatalf("stdout write: %v", err)
	}

	done := make(chan struct{})
	go func() {
		capture.Stop()
		close(done)
	}()

	select {
	case got := <-lines:
		if got != "tail" {
			t.Fatalf("captured tail = %q, want %q", got, "tail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for captured tail line")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() blocked while flushing tail")
	}
}

func TestSanitizeOverlayOutputStripsANSISequences(t *testing.T) {
	t.Parallel()

	got := sanitizeOverlayOutput("\x1b[31mred\x1b[0m \x1b]2;title\x07plain")
	if want := "red plain"; got != want {
		t.Fatalf("sanitizeOverlayOutput() = %q, want %q", got, want)
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
	if got, want := overlayHeight(overlay), 3; got != want {
		t.Fatalf("overlayHeight(empty) = %d, want %d", got, want)
	}

	for i := 0; i < 20; i++ {
		overlay.addOutput("line")
	}
	if got, want := overlayHeight(overlay), termRows/2; got != want {
		t.Fatalf("overlayHeight(full) = %d, want %d", got, want)
	}

	overlay.setRunning(true)
	if got, want := overlayHeight(overlay), termRows/2; got != want {
		t.Fatalf("overlayHeight(running) = %d, want %d", got, want)
	}
}

func TestOverlayHeightHidesPromptRowWhileRunning(t *testing.T) {
	prev := termRows
	termRows = 10
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addCommand("!sleep 5")
	if got, want := overlayHeight(overlay), 4; got != want {
		t.Fatalf("overlayHeight(idle) = %d, want %d", got, want)
	}

	overlay.setRunning(true)
	if got, want := overlayHeight(overlay), 3; got != want {
		t.Fatalf("overlayHeight(running) = %d, want %d", got, want)
	}
	if got, want := overlayHistoryRows(overlay), 1; got != want {
		t.Fatalf("overlayHistoryRows(running) = %d, want %d", got, want)
	}
}

func TestOverlayRenderLinesRespectsScrollback(t *testing.T) {
	prev := termRows
	termRows = 6
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	overlay.visible = true
	for i := 0; i < 6; i++ {
		overlay.addOutput(string(rune('a' + i)))
	}

	if got, want := overlayTexts(overlay.renderLines(3)), []string{"█ d", "█ e", "█ f"}; !equalStrings(got, want) {
		t.Fatalf("renderLines(tail) = %q, want %q", got, want)
	}

	overlay.scrollOlder(2)
	if got, want := overlayTexts(overlay.renderLines(3)), []string{"█ b", "█ c", "█ d"}; !equalStrings(got, want) {
		t.Fatalf("renderLines(scrolled) = %q, want %q", got, want)
	}

	overlay.scrollNewer(1)
	if got, want := overlayTexts(overlay.renderLines(3)), []string{"█ c", "█ d", "█ e"}; !equalStrings(got, want) {
		t.Fatalf("renderLines(partial return) = %q, want %q", got, want)
	}
}

func TestOverlayScreenToPosMapsRenderedRows(t *testing.T) {
	prev := termRows
	termRows = 10
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addOutput("alpha")
	overlay.addCommand("b")

	pos := overlay.screenToPos(overlayTopRow(overlay)+1, 2)
	if pos.line != 0 || pos.col != 0 {
		t.Fatalf("screenToPos(output gutter) = (%d, %d), want (0, 0)", pos.line, pos.col)
	}

	pos = overlay.screenToPos(overlayTopRow(overlay)+1, 4)
	if pos.line != 0 || pos.col != 2 {
		t.Fatalf("screenToPos(output text) = (%d, %d), want (0, 2)", pos.line, pos.col)
	}

	pos = overlay.screenToPos(overlayTopRow(overlay)+2, 1)
	if pos.line != 1 || pos.col != 1 {
		t.Fatalf("screenToPos(command) = (%d, %d), want (1, 1)", pos.line, pos.col)
	}
}

func TestOverlaySelectedTextSpansLines(t *testing.T) {
	overlay := newOverlayState()
	overlay.history = []overlayEntry{
		{text: "alpha"},
		{command: true, text: "beta"},
	}
	overlay.selectStart = overlaySelectionPos{line: 0, col: 2}
	overlay.selectEnd = overlaySelectionPos{line: 1, col: 2}

	if got, want := string(overlay.selectedText()), "pha\nbe"; got != want {
		t.Fatalf("selectedText() = %q, want %q", got, want)
	}
}

func TestOverlayTokenAtTrimsSuffixGarbage(t *testing.T) {
	overlay := newOverlayState()
	overlay.history = []overlayEntry{
		{text: "src/main.go:29:21:use more"},
	}

	if got, want := overlay.tokenAt(overlaySelectionPos{line: 0, col: 14}), "src/main.go:29:21"; got != want {
		t.Fatalf("tokenAt() = %q, want %q", got, want)
	}
}

func TestTrimOverlaySelection(t *testing.T) {
	if got, want := trimOverlaySelection([]rune("  alpha beta \n")), "alpha beta"; got != want {
		t.Fatalf("trimOverlaySelection() = %q, want %q", got, want)
	}
}

func TestIsOverlayClickSelection(t *testing.T) {
	t.Parallel()

	if !isOverlayClickSelection(
		overlaySelectionPos{line: 1, col: 4},
		overlaySelectionPos{line: 1, col: 5},
	) {
		t.Fatalf("isOverlayClickSelection() = false, want true for tiny same-line wobble")
	}
	if isOverlayClickSelection(
		overlaySelectionPos{line: 1, col: 4},
		overlaySelectionPos{line: 2, col: 4},
	) {
		t.Fatalf("isOverlayClickSelection() = true, want false across lines")
	}
}

func TestOverlayRenderLinesMarksLatestCommandAsRunning(t *testing.T) {
	prev := termRows
	termRows = 10
	t.Cleanup(func() {
		termRows = prev
	})

	overlay := newOverlayState()
	overlay.visible = true
	overlay.addCommand(",p")
	overlay.addOutput("alpha")
	overlay.setRunning(true)

	lines := overlay.renderLines(3)
	if got, want := overlayTexts(lines), []string{",p", "█ alpha"}; !equalStrings(got, want) {
		t.Fatalf("renderLines(running) = %q, want %q", got, want)
	}
	if !lines[0].running {
		t.Fatalf("command running flag = false, want true")
	}
	if lines[1].running {
		t.Fatalf("output running flag = true, want false")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func overlayTexts(lines []overlayRenderLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.text)
	}
	return out
}
