package term

import (
	"fmt"
	"io"
	"os"
	"strings"
)

type frameCursor struct {
	visible bool
	row     int
	col     int
}

type frameTerminalState struct {
	altScreen      bool
	mousePress     bool
	mouseMotion    bool
	focusReporting bool
	mouseSGR       bool
	bracketedPaste bool
	cursorShape    frameCursorShape
}

type frameCursorShape int

const (
	frameCursorShapeBlock frameCursorShape = iota
	frameCursorShapeBar
)

type redrawClass string

const (
	redrawInitial         redrawClass = "initial"
	redrawBufferCursor    redrawClass = "buffer_cursor"
	redrawBufferSelection redrawClass = "buffer_selection"
	redrawBufferViewport  redrawClass = "buffer_viewport"
	redrawBufferContent   redrawClass = "buffer_content"
	redrawBufferStatus    redrawClass = "buffer_status"
	redrawOverlayInput    redrawClass = "overlay_input"
	redrawOverlayHistory  redrawClass = "overlay_history"
	redrawOverlayOpen     redrawClass = "overlay_open"
	redrawOverlayClose    redrawClass = "overlay_close"
	redrawMenuHover       redrawClass = "menu_hover"
	redrawMenuOpen        redrawClass = "menu_open"
	redrawMenuClose       redrawClass = "menu_close"
	redrawTheme           redrawClass = "theme"
	redrawRefresh         redrawClass = "refresh"
	redrawResize          redrawClass = "resize"
	redrawRecover         redrawClass = "recover"
)

type frameRenderStats struct {
	enabled bool
	out     io.Writer
	counts  map[redrawClass]*frameRenderAggregate
}

type frameRenderAggregate struct {
	renders int
	full    int
	diff    int
	rows    int
	bytes   int
}

type frameRenderResult struct {
	full  bool
	rows  int
	bytes int
}

type countingWriter struct {
	w     io.Writer
	count int
}

func defaultFrameTerminalState() frameTerminalState {
	return frameTerminalState{
		altScreen:      true,
		mousePress:     true,
		mouseMotion:    true,
		focusReporting: true,
		mouseSGR:       true,
		bracketedPaste: true,
		cursorShape:    frameCursorShapeBar,
	}
}

func newFrameRenderStats(out io.Writer) *frameRenderStats {
	return &frameRenderStats{
		enabled: strings.TrimSpace(os.Getenv("ION_RENDER_TRACE")) != "",
		out:     out,
		counts:  make(map[redrawClass]*frameRenderAggregate),
	}
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.count += n
	return n, err
}

func (s *frameRenderStats) Record(class redrawClass, result frameRenderResult) {
	if s == nil || !s.enabled {
		return
	}
	agg := s.counts[class]
	if agg == nil {
		agg = &frameRenderAggregate{}
		s.counts[class] = agg
	}
	agg.renders++
	if result.full {
		agg.full++
	} else {
		agg.diff++
	}
	agg.rows += result.rows
	agg.bytes += result.bytes
}

func (s *frameRenderStats) Report() error {
	if s == nil || !s.enabled || s.out == nil {
		return nil
	}
	order := []redrawClass{
		redrawInitial,
		redrawRecover,
		redrawResize,
		redrawRefresh,
		redrawBufferCursor,
		redrawBufferSelection,
		redrawBufferViewport,
		redrawBufferContent,
		redrawBufferStatus,
		redrawOverlayOpen,
		redrawOverlayInput,
		redrawOverlayHistory,
		redrawOverlayClose,
		redrawMenuOpen,
		redrawMenuHover,
		redrawMenuClose,
		redrawTheme,
	}
	if _, err := io.WriteString(s.out, "ion: render stats\n"); err != nil {
		return err
	}
	for _, class := range order {
		agg := s.counts[class]
		if agg == nil || agg.renders == 0 {
			continue
		}
		if _, err := fmt.Fprintf(s.out, "  %s: renders=%d full=%d diff=%d rows=%d bytes=%d\n", class, agg.renders, agg.full, agg.diff, agg.rows, agg.bytes); err != nil {
			return err
		}
	}
	return nil
}
