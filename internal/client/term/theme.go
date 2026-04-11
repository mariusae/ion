package term

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type colorMode int

const (
	colorModeNone colorMode = iota
	colorModeANSI256
	colorModeTrueColor
)

type rgbColor struct {
	r uint8
	g uint8
	b uint8
}

type uiTheme struct {
	mode        colorMode
	subtleBG    rgbColor
	hudBG       rgbColor
	outputBG    rgbColor
	titleBG     rgbColor
	highlightBG rgbColor
	cursorBG    rgbColor
}

type terminalThemeReport struct {
	mode           colorMode
	modeSource     string
	queryStatus    string
	queryReason    string
	queryMethod    string
	queryTimeout   time.Duration
	inputSource    string
	outputSource   string
	rawResponse    []byte
	background     rgbColor
	backgroundOK   bool
	backgroundFrom string
	theme          *uiTheme
}

func detectTerminalTheme(stdin *os.File, stdout io.Writer) (*uiTheme, []byte) {
	report, prefix := collectTerminalThemeReport(stdin, stdout)
	return report.theme, prefix
}

type terminalThemeDetector func(stdin *os.File, stdout io.Writer) (*uiTheme, []byte)

func refreshTerminalThemeWithDetector(stdin *os.File, stdout io.Writer, reader *bufio.Reader, detect terminalThemeDetector) (*uiTheme, *bufio.Reader) {
	nextTheme, prefetched := detect(stdin, stdout)
	if len(prefetched) > 0 {
		reader = bufio.NewReader(io.MultiReader(bytes.NewReader(prefetched), reader))
	}
	return nextTheme, reader
}

func refreshTerminalTheme(stdin *os.File, stdout io.Writer, reader *bufio.Reader) (*uiTheme, *bufio.Reader) {
	return refreshTerminalThemeWithDetector(stdin, stdout, reader, detectTerminalTheme)
}

func detectColorMode() colorMode {
	mode, _ := detectColorModeWithSource()
	return mode
}

func detectColorModeWithSource() (colorMode, string) {
	colorterm := strings.ToLower(os.Getenv("COLORTERM"))
	term := strings.ToLower(os.Getenv("TERM"))
	inTmux := strings.TrimSpace(os.Getenv("TMUX")) != ""
	if strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return colorModeTrueColor, fmt.Sprintf("COLORTERM=%q", os.Getenv("COLORTERM"))
	}
	if strings.Contains(term, "direct") {
		return colorModeTrueColor, fmt.Sprintf("TERM=%q", os.Getenv("TERM"))
	}
	if strings.Contains(term, "256color") {
		if inTmux {
			return colorModeTrueColor, fmt.Sprintf("TERM=%q with TMUX set", os.Getenv("TERM"))
		}
		return colorModeANSI256, fmt.Sprintf("TERM=%q", os.Getenv("TERM"))
	}
	if os.Getenv("COLORTERM") != "" {
		return colorModeNone, fmt.Sprintf("COLORTERM=%q", os.Getenv("COLORTERM"))
	}
	if os.Getenv("TERM") != "" {
		return colorModeNone, fmt.Sprintf("TERM=%q", os.Getenv("TERM"))
	}
	return colorModeNone, "COLORTERM and TERM unset"
}

func buildTheme(bg rgbColor, mode colorMode) *uiTheme {
	light := luminance(bg) > 128
	return &uiTheme{
		mode:        mode,
		subtleBG:    blendTint(bg, light, alphaFor(light, 0.04, 0.12)),
		hudBG:       blendTint(bg, light, alphaFor(light, 0.04, 0.12)),
		outputBG:    blendTint(bg, light, alphaFor(light, 0.12, 0.24)),
		titleBG:     blendTint(bg, light, alphaFor(light, 0.14, 0.26)),
		highlightBG: blendTint(bg, light, alphaFor(light, 0.10, 0.20)),
		cursorBG:    blendTint(bg, light, alphaFor(light, 0.26, 0.38)),
	}
}

func collectTerminalThemeReport(stdin *os.File, stdout io.Writer) (terminalThemeReport, []byte) {
	mode, modeSource := detectColorModeWithSource()
	report := terminalThemeReport{
		mode:         mode,
		modeSource:   modeSource,
		queryTimeout: 250 * time.Millisecond,
	}
	if mode == colorModeNone {
		report.queryStatus = "skipped"
		report.queryReason = "color mode detection disabled theme probing"
		return report, nil
	}
	if stdin == nil {
		report.queryStatus = "skipped"
		report.queryReason = "stdin is not an *os.File tty handle"
		return report, nil
	}
	if stdout == nil {
		report.queryStatus = "skipped"
		report.queryReason = "stdout is nil"
		return report, nil
	}
	bg, prefix, query := queryTerminalBackground(stdin, stdout, report.queryTimeout)
	report.queryStatus = query.status
	report.queryReason = query.reason
	report.queryMethod = query.method
	report.inputSource = query.inputSource
	report.outputSource = query.outputSource
	report.rawResponse = append(report.rawResponse, query.raw...)
	if query.ok {
		report.background = bg
		report.backgroundOK = true
		report.backgroundFrom = query.method
		report.theme = buildTheme(bg, mode)
	}
	return report, prefix
}

func sameTheme(a, b *uiTheme) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func alphaFor(light bool, lightAlpha, darkAlpha float64) float64 {
	if light {
		return lightAlpha
	}
	return darkAlpha
}

func WriteThemeDiagnostics(stdin io.Reader, stdout io.Writer) error {
	var stdinFile *os.File
	if f, ok := stdin.(*os.File); ok {
		stdinFile = f
	}
	report, _ := collectTerminalThemeReport(stdinFile, stdout)
	_, err := io.WriteString(stdout, formatThemeDiagnostics(report))
	return err
}

func formatThemeDiagnostics(report terminalThemeReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, "ion terminal diagnostics")
	fmt.Fprintf(&b, "color_mode: %s\n", report.mode.String())
	fmt.Fprintf(&b, "color_mode_source: %s\n", report.modeSource)
	fmt.Fprintf(&b, "query_status: %s\n", report.queryStatus)
	if report.queryReason != "" {
		fmt.Fprintf(&b, "query_reason: %s\n", report.queryReason)
	}
	if report.queryMethod != "" {
		fmt.Fprintf(&b, "query_method: %s\n", report.queryMethod)
	}
	if report.inputSource != "" {
		fmt.Fprintf(&b, "query_input: %s\n", report.inputSource)
	}
	if report.outputSource != "" {
		fmt.Fprintf(&b, "query_output: %s\n", report.outputSource)
	}
	if report.queryTimeout > 0 {
		fmt.Fprintf(&b, "query_timeout_ms: %d\n", report.queryTimeout.Milliseconds())
	}
	if len(report.rawResponse) > 0 {
		fmt.Fprintf(&b, "query_raw: %q\n", string(report.rawResponse))
		fmt.Fprintf(&b, "query_raw_hex: %x\n", report.rawResponse)
	}
	fmt.Fprintf(&b, "theme_enabled: %t\n", report.theme != nil)
	if !report.backgroundOK {
		return b.String()
	}
	light := luminance(report.background) > 128
	fmt.Fprintf(&b, "background_method: %s\n", report.backgroundFrom)
	fmt.Fprintf(&b, "background_rgb: %s\n", formatRGB(report.background))
	fmt.Fprintf(&b, "background_luminance: %.2f\n", luminance(report.background))
	fmt.Fprintf(&b, "background_is_light: %t\n", light)
	writeTintDiagnostics(&b, "subtle_bg", report.theme.subtleBG, report.theme)
	writeTintDiagnostics(&b, "hud_bg", report.theme.hudBG, report.theme)
	writeTintDiagnostics(&b, "output_bg", report.theme.outputBG, report.theme)
	writeTintDiagnostics(&b, "title_bg", report.theme.titleBG, report.theme)
	writeTintDiagnostics(&b, "highlight_bg", report.theme.highlightBG, report.theme)
	writeTintDiagnostics(&b, "cursor_bg", report.theme.cursorBG, report.theme)
	return b.String()
}

func writeTintDiagnostics(b *strings.Builder, name string, color rgbColor, theme *uiTheme) {
	if b == nil || theme == nil {
		return
	}
	fmt.Fprintf(b, "tint.%s: %s sgr=%s\n", name, formatRGB(color), theme.bgCode(color))
}

func formatRGB(color rgbColor) string {
	return fmt.Sprintf("#%02x%02x%02x (%d,%d,%d)", color.r, color.g, color.b, color.r, color.g, color.b)
}

func luminance(c rgbColor) float64 {
	return 0.299*float64(c.r) + 0.587*float64(c.g) + 0.114*float64(c.b)
}

func blendTint(bg rgbColor, light bool, alpha float64) rgbColor {
	overlay := 255.0
	if light {
		overlay = 0.0
	}
	blend := func(ch uint8) uint8 {
		value := math.Floor(overlay*alpha + float64(ch)*(1.0-alpha))
		if value < 0 {
			value = 0
		}
		if value > 255 {
			value = 255
		}
		return uint8(value)
	}
	return rgbColor{
		r: blend(bg.r),
		g: blend(bg.g),
		b: blend(bg.b),
	}
}

type terminalQueryResult struct {
	status       string
	reason       string
	method       string
	inputSource  string
	outputSource string
	raw          []byte
	ok           bool
}

func queryTerminalBackground(stdin *os.File, stdout io.Writer, timeout time.Duration) (rgbColor, []byte, terminalQueryResult) {
	input, inputSource, inputOwned, err := openQueryInput(stdin)
	if err != nil {
		return rgbColor{}, nil, terminalQueryResult{
			status: "setup_failed",
			reason: err.Error(),
			method: "osc-11",
		}
	}
	if inputOwned {
		defer input.Close()
	}

	output, outputSource, outputOwned, err := openQueryOutput(stdout)
	if err != nil {
		return rgbColor{}, nil, terminalQueryResult{
			status:      "setup_failed",
			reason:      err.Error(),
			method:      "osc-11",
			inputSource: inputSource,
		}
	}
	if outputOwned {
		defer output.Close()
	}

	state, err := enterCBreakMode(input)
	if err != nil {
		return rgbColor{}, nil, terminalQueryResult{
			status:       "setup_failed",
			reason:       err.Error(),
			method:       "osc-11",
			inputSource:  inputSource,
			outputSource: outputSource,
		}
	}
	defer state.restore()

	if _, err := io.WriteString(output, "\x1b]11;?\x1b\\"); err != nil {
		return rgbColor{}, nil, terminalQueryResult{
			status:       "write_failed",
			reason:       err.Error(),
			method:       "osc-11",
			inputSource:  inputSource,
			outputSource: outputSource,
		}
	}

	fd := int(input.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return rgbColor{}, nil, terminalQueryResult{
			status:       "setup_failed",
			reason:       err.Error(),
			method:       "osc-11",
			inputSource:  inputSource,
			outputSource: outputSource,
		}
	}
	defer func() {
		_ = syscall.SetNonblock(fd, false)
	}()

	deadline := time.Now().Add(timeout)
	var raw []byte
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := input.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			color, rest, found, ok := extractTerminalBackgroundResponse(raw)
			if found {
				status := "parse_failed"
				reason := "terminal responded to OSC 11 with an unparseable payload"
				if ok {
					status = "ok"
					reason = ""
				}
				return color, rest, terminalQueryResult{
					status:       status,
					reason:       reason,
					method:       "osc-11",
					inputSource:  inputSource,
					outputSource: outputSource,
					raw:          append([]byte(nil), raw...),
					ok:           ok,
				}
			}
		}
		if err == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if err == io.EOF {
			break
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if pathErr, ok := err.(*os.PathError); ok {
			if pathErr.Err == syscall.EAGAIN || pathErr.Err == syscall.EWOULDBLOCK {
				time.Sleep(5 * time.Millisecond)
				continue
			}
		}
		break
	}
	return rgbColor{}, raw, terminalQueryResult{
		status:       "no_response",
		reason:       "terminal did not answer OSC 11 before timeout",
		method:       "osc-11",
		inputSource:  inputSource,
		outputSource: outputSource,
		raw:          append([]byte(nil), raw...),
	}
}

func openQueryInput(stdin *os.File) (*os.File, string, bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err == nil {
		return tty, "/dev/tty", true, nil
	}
	if stdin != nil && isTTY(stdin) {
		return stdin, "stdin", false, nil
	}
	return nil, "", false, err
}

func openQueryOutput(stdout io.Writer) (*os.File, string, bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err == nil {
		return tty, "/dev/tty", true, nil
	}
	file, ok := stdout.(*os.File)
	if ok && isTTY(file) {
		return file, "stdout", false, nil
	}
	return nil, "", false, err
}

func extractTerminalBackgroundResponse(data []byte) (rgbColor, []byte, bool, bool) {
	if strings.Contains(string(data), "\x1bPtmux;") {
		if color, rest, found, ok := extractTmuxWrappedOSC11Response(data); found {
			return color, rest, found, ok
		}
	}
	if color, rest, found, ok := extractOSC11Response(data); found {
		return color, rest, found, ok
	}
	return rgbColor{}, data, false, false
}

func extractOSC11Response(data []byte) (rgbColor, []byte, bool, bool) {
	const prefix = "\x1b]11;"
	start := strings.Index(string(data), prefix)
	if start < 0 {
		return rgbColor{}, data, false, false
	}
	payloadStart := start + len(prefix)
	for i := payloadStart; i < len(data); i++ {
		switch data[i] {
		case 0x07:
			color, ok := parseOSCColorPayload(string(data[payloadStart:i]))
			return color, spliceBytes(data, start, i+1), true, ok
		case 0x1b:
			if i+1 < len(data) && data[i+1] == '\\' {
				color, ok := parseOSCColorPayload(string(data[payloadStart:i]))
				return color, spliceBytes(data, start, i+2), true, ok
			}
		}
	}
	return rgbColor{}, data, false, false
}

func extractTmuxWrappedOSC11Response(data []byte) (rgbColor, []byte, bool, bool) {
	start := strings.Index(string(data), "\x1bPtmux;")
	if start < 0 {
		return rgbColor{}, data, false, false
	}
	payloadStart := start + len("\x1bPtmux;")
	for i := payloadStart; i+1 < len(data); i++ {
		if data[i] == 0x1b && data[i+1] == '\\' && (i == payloadStart || data[i-1] != 0x1b) {
			payload := string(data[payloadStart:i])
			payload = strings.ReplaceAll(payload, "\x1b\x1b", "\x1b")
			color, ok := parseWrappedOSC11Payload(payload)
			return color, spliceBytes(data, start, i+2), true, ok
		}
	}
	return rgbColor{}, data, false, false
}

func parseWrappedOSC11Payload(payload string) (rgbColor, bool) {
	if strings.HasPrefix(payload, "\x1b]11;") {
		payload = strings.TrimPrefix(payload, "\x1b]11;")
		switch {
		case strings.HasSuffix(payload, "\x07"):
			payload = strings.TrimSuffix(payload, "\x07")
		case strings.HasSuffix(payload, "\x1b\\"):
			payload = strings.TrimSuffix(payload, "\x1b\\")
		}
	}
	return parseOSCColorPayload(payload)
}

func spliceBytes(data []byte, start, end int) []byte {
	out := make([]byte, 0, len(data)-(end-start))
	out = append(out, data[:start]...)
	out = append(out, data[end:]...)
	return out
}

func parseOSCColorPayload(payload string) (rgbColor, bool) {
	var parts []string
	switch {
	case strings.HasPrefix(payload, "rgb:"):
		parts = strings.Split(payload[4:], "/")
		if len(parts) != 3 {
			return rgbColor{}, false
		}
	case strings.HasPrefix(payload, "rgba:"):
		parts = strings.Split(payload[5:], "/")
		if len(parts) != 4 {
			return rgbColor{}, false
		}
		parts = parts[:3]
	default:
		return rgbColor{}, false
	}

	r, ok := parseColorComponent(parts[0])
	if !ok {
		return rgbColor{}, false
	}
	g, ok := parseColorComponent(parts[1])
	if !ok {
		return rgbColor{}, false
	}
	b, ok := parseColorComponent(parts[2])
	if !ok {
		return rgbColor{}, false
	}
	return rgbColor{r: r, g: g, b: b}, true
}

func parseColorComponent(part string) (uint8, bool) {
	switch len(part) {
	case 2:
		v, err := strconv.ParseUint(part, 16, 8)
		return uint8(v), err == nil
	case 4:
		v, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return 0, false
		}
		return uint8(v / 257), true
	default:
		return 0, false
	}
}

func (t *uiTheme) subtlePrefix() string {
	if t == nil {
		return ""
	}
	return t.prefixFor(t.subtleBG)
}

func (t *uiTheme) hudPrefix() string {
	if t == nil {
		return ""
	}
	return t.prefixFor(t.hudBG)
}

func (t *uiTheme) outputPrefix() string {
	if t == nil {
		return ""
	}
	return t.prefixFor(t.outputBG)
}

func (t *uiTheme) commandPrefix() string {
	if t == nil {
		return ""
	}
	return sgr("1", t.bgCode(t.hudBG))
}

func (t *uiTheme) pickerSelectionPrefix() string {
	if t == nil {
		return ""
	}
	return sgr("1", t.bgCode(t.outputBG))
}

func (t *uiTheme) titlePrefix() string {
	if t == nil {
		return ""
	}
	return sgr("1", t.bgCode(t.titleBG))
}

func (t *uiTheme) hoverPrefix() string {
	if t == nil {
		return ""
	}
	return sgr("1", t.bgCode(t.cursorBG))
}

func (t *uiTheme) selectionPrefix() string {
	if t == nil {
		return ""
	}
	return t.prefixFor(t.highlightBG)
}

func (t *uiTheme) cursorPrefix() string {
	if t == nil {
		return ""
	}
	return sgr("1", t.bgCode(t.cursorBG))
}

func (t *uiTheme) prefixFor(bg rgbColor) string {
	return sgr(t.bgCode(bg))
}

func (t *uiTheme) bgCode(bg rgbColor) string {
	if t == nil {
		return ""
	}
	switch t.mode {
	case colorModeTrueColor:
		return fmt.Sprintf("48;2;%d;%d;%d", bg.r, bg.g, bg.b)
	case colorModeANSI256:
		return fmt.Sprintf("48;5;%d", nearestANSI256(bg))
	default:
		return ""
	}
}

func (t *uiTheme) fgCode(fg rgbColor) string {
	if t == nil {
		return ""
	}
	switch t.mode {
	case colorModeTrueColor:
		return fmt.Sprintf("38;2;%d;%d;%d", fg.r, fg.g, fg.b)
	case colorModeANSI256:
		return fmt.Sprintf("38;5;%d", nearestANSI256(fg))
	default:
		return ""
	}
}

func nearestANSI256(c rgbColor) int {
	bestIndex := 0
	bestDist := math.MaxFloat64
	for i := 16; i < 256; i++ {
		r, g, b := ansi256RGB(i)
		dr := float64(int(c.r) - int(r))
		dg := float64(int(c.g) - int(g))
		db := float64(int(c.b) - int(b))
		dist := 0.299*dr*dr + 0.587*dg*dg + 0.114*db*db
		if dist < bestDist {
			bestDist = dist
			bestIndex = i
		}
	}
	return bestIndex
}

func ansi256RGB(index int) (uint8, uint8, uint8) {
	if index < 16 {
		base := [16]rgbColor{
			{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
			{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
			{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
			{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
		}
		c := base[index]
		return c.r, c.g, c.b
	}
	if index >= 232 {
		level := uint8(8 + (index-232)*10)
		return level, level, level
	}
	index -= 16
	levels := [6]uint8{0, 95, 135, 175, 215, 255}
	r := levels[index/36]
	g := levels[(index/6)%6]
	b := levels[index%6]
	return r, g, b
}

func sgr(codes ...string) string {
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if code == "" {
			continue
		}
		out = append(out, code)
	}
	if len(out) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(out, ";") + "m"
}

func styleReset() string {
	return "\x1b[39;49;22;24;27m"
}

func (m colorMode) String() string {
	switch m {
	case colorModeANSI256:
		return "ansi256"
	case colorModeTrueColor:
		return "truecolor"
	default:
		return "none"
	}
}
