package term

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDetectColorModeWithSourceTreatsTmux256AsTrueColor(t *testing.T) {
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("TMUX", "/tmp/tmux-123/default,999,0")
	t.Setenv("COLORTERM", "")

	mode, source := detectColorModeWithSource()
	if got, want := mode, colorModeTrueColor; got != want {
		t.Fatalf("mode = %v, want %v", got, want)
	}
	if got, want := source, `TERM="tmux-256color" with TMUX set`; got != want {
		t.Fatalf("source = %q, want %q", got, want)
	}
}

func TestDetectColorModeWithSourceTreatsDirectAsTrueColor(t *testing.T) {
	t.Setenv("TERM", "xterm-direct")
	t.Setenv("TMUX", "")
	t.Setenv("COLORTERM", "")

	mode, source := detectColorModeWithSource()
	if got, want := mode, colorModeTrueColor; got != want {
		t.Fatalf("mode = %v, want %v", got, want)
	}
	if got, want := source, `TERM="xterm-direct"`; got != want {
		t.Fatalf("source = %q, want %q", got, want)
	}
}

func TestFormatThemeDiagnosticsIncludesComputedTints(t *testing.T) {
	t.Parallel()

	report := terminalThemeReport{
		mode:           colorModeTrueColor,
		modeSource:     "COLORTERM=truecolor",
		queryStatus:    "ok",
		queryMethod:    "osc-11",
		queryTimeout:   75 * time.Millisecond,
		rawResponse:    []byte("\x1b]11;rgb:ffff/ffff/ffff\x1b\\"),
		background:     rgbColor{r: 255, g: 255, b: 255},
		backgroundOK:   true,
		backgroundFrom: "osc-11",
		theme:          buildTheme(rgbColor{r: 255, g: 255, b: 255}, colorModeTrueColor),
	}

	got := formatThemeDiagnostics(report)
	for _, want := range []string{
		"color_mode: truecolor",
		"background_method: osc-11",
		"background_rgb: #ffffff (255,255,255)",
		"background_is_light: true",
		"tint.hud_bg: #f4f4f4 (244,244,244) sgr=48;2;244;244;244",
		"tint.output_bg: #e0e0e0 (224,224,224) sgr=48;2;224;224;224",
		`query_raw: "\x1b]11;rgb:ffff/ffff/ffff\x1b\\"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatThemeDiagnostics() missing %q in %q", want, got)
		}
	}
}

func TestFormatThemeDiagnosticsReportsSkippedQuery(t *testing.T) {
	t.Parallel()

	report := terminalThemeReport{
		mode:        colorModeNone,
		modeSource:  "TERM=dumb",
		queryStatus: "skipped",
		queryReason: "color mode detection disabled theme probing",
	}

	got := formatThemeDiagnostics(report)
	for _, want := range []string{
		"color_mode: none",
		"query_status: skipped",
		"query_reason: color mode detection disabled theme probing",
		"theme_enabled: false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatThemeDiagnostics() missing %q in %q", want, got)
		}
	}
}

func TestRefreshTerminalThemePrependsPrefetchedBytes(t *testing.T) {
	t.Parallel()

	reader := bufio.NewReader(strings.NewReader("tail"))
	wantTheme := buildTheme(rgbColor{r: 1, g: 2, b: 3}, colorModeTrueColor)
	detect := func(stdin *os.File, stdout io.Writer) (*uiTheme, []byte) {
		return wantTheme, []byte("head")
	}

	gotTheme, gotReader := refreshTerminalThemeWithDetector(nil, io.Discard, reader, detect)
	if !sameTheme(gotTheme, wantTheme) {
		t.Fatalf("theme = %#v, want %#v", gotTheme, wantTheme)
	}
	got, err := io.ReadAll(gotReader)
	if err != nil {
		t.Fatalf("ReadAll(reader) error = %v", err)
	}
	if got, want := string(got), "headtail"; got != want {
		t.Fatalf("reader contents = %q, want %q", got, want)
	}
}

func TestExtractTerminalBackgroundResponseParsesTmuxWrappedOSC11(t *testing.T) {
	t.Parallel()

	raw := []byte("\x1bPtmux;\x1b\x1b]11;rgb:ffff/f1f1/e5e5\x1b\x1b\\\x1b\\abc")
	color, rest, found, ok := extractTerminalBackgroundResponse(raw)
	if !found || !ok {
		t.Fatalf("extractTerminalBackgroundResponse() found=%t ok=%t, want true true", found, ok)
	}
	if got, want := color, (rgbColor{r: 255, g: 241, b: 229}); got != want {
		t.Fatalf("color = %#v, want %#v", got, want)
	}
	if got, want := string(rest), "abc"; got != want {
		t.Fatalf("rest = %q, want %q", got, want)
	}
}
