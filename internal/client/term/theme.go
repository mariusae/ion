package term

import (
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

func detectTerminalTheme(stdin *os.File, stdout io.Writer) (*uiTheme, []byte) {
	mode := detectColorMode()
	if mode == colorModeNone || stdin == nil || stdout == nil {
		return nil, nil
	}
	bg, prefix, ok := queryTerminalBackground(stdin, stdout, 75*time.Millisecond)
	if !ok {
		return nil, prefix
	}
	return buildTheme(bg, mode), prefix
}

func detectColorMode() colorMode {
	colorterm := strings.ToLower(os.Getenv("COLORTERM"))
	if strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return colorModeTrueColor
	}
	term := strings.ToLower(os.Getenv("TERM"))
	if strings.Contains(term, "256color") {
		return colorModeANSI256
	}
	return colorModeNone
}

func buildTheme(bg rgbColor, mode colorMode) *uiTheme {
	light := luminance(bg) > 128
	return &uiTheme{
		mode:        mode,
		subtleBG:    blendTint(bg, light, alphaFor(light, 0.04, 0.12)),
		hudBG:       blendTint(bg, light, alphaFor(light, 0.10, 0.20)),
		outputBG:    blendTint(bg, light, alphaFor(light, 0.04, 0.12)),
		titleBG:     blendTint(bg, light, alphaFor(light, 0.14, 0.26)),
		highlightBG: blendTint(bg, light, alphaFor(light, 0.10, 0.20)),
		cursorBG:    blendTint(bg, light, alphaFor(light, 0.16, 0.30)),
	}
}

func alphaFor(light bool, lightAlpha, darkAlpha float64) float64 {
	if light {
		return lightAlpha
	}
	return darkAlpha
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

func queryTerminalBackground(stdin *os.File, stdout io.Writer, timeout time.Duration) (rgbColor, []byte, bool) {
	if _, err := io.WriteString(stdout, "\x1b]11;?\x1b\\"); err != nil {
		return rgbColor{}, nil, false
	}

	fd := int(stdin.Fd())
	if err := syscall.SetNonblock(fd, true); err != nil {
		return rgbColor{}, nil, false
	}
	defer func() {
		_ = syscall.SetNonblock(fd, false)
	}()

	deadline := time.Now().Add(timeout)
	var raw []byte
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := stdin.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			color, rest, found, ok := extractOSC11Response(raw)
			if found {
				return color, rest, ok
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
	return rgbColor{}, raw, false
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
	return "\x1b[49;22;24;27m"
}
