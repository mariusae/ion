package session

import (
	"fmt"
	"io"
	"strings"

	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
)

type navigationPoint struct {
	fileID   int
	name     string
	dotStart int
	dotEnd   int
}

type navigationStack struct {
	points []navigationPoint
	index  int
}

func navigationPointFromView(view wire.BufferView) (navigationPoint, bool) {
	name := strings.TrimSpace(view.Name)
	if view.ID == 0 && name == "" {
		return navigationPoint{}, false
	}
	return navigationPoint{
		fileID:   view.ID,
		name:     name,
		dotStart: view.DotStart,
		dotEnd:   view.DotEnd,
	}, true
}

func sameNavigationPoint(a, b navigationPoint) bool {
	if a.fileID != 0 && b.fileID != 0 {
		if a.fileID != b.fileID {
			return false
		}
	} else if a.name != b.name {
		return false
	}
	return a.dotStart == b.dotStart && a.dotEnd == b.dotEnd
}

func (s *navigationStack) Record(point navigationPoint) {
	if s == nil {
		return
	}
	if len(s.points) == 0 {
		s.points = []navigationPoint{point}
		s.index = 0
		return
	}
	if s.index < 0 {
		s.index = 0
	}
	if s.index >= len(s.points) {
		s.index = len(s.points) - 1
	}
	if sameNavigationPoint(s.points[s.index], point) {
		return
	}
	s.points = append(s.points[:s.index+1], point)
	s.index = len(s.points) - 1
}

func (s *navigationStack) Target(delta int) (navigationPoint, int, bool) {
	if s == nil || len(s.points) == 0 || delta == 0 {
		return navigationPoint{}, 0, false
	}
	next := s.index + delta
	if next < 0 || next >= len(s.points) {
		return navigationPoint{}, 0, false
	}
	return s.points[next], next, true
}

func shouldRecordCommandNavigation(cmd *cmdlang.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch cmd.Cmdc {
	case '\n', 'p', 'b', 'B', 'e':
		return true
	default:
		return false
	}
}

func (s *DownloadSession) recordCurrentView() error {
	if s == nil {
		return nil
	}
	view, err := s.ws.CurrentView()
	if err != nil {
		return err
	}
	s.recordView(view)
	return nil
}

func (s *DownloadSession) recordView(view wire.BufferView) {
	if s == nil {
		return
	}
	if point, ok := navigationPointFromView(view); ok {
		s.history.Record(point)
	}
}

func (s *DownloadSession) navigate(delta int) (bool, error) {
	target, nextIndex, ok := s.history.Target(delta)
	if !ok {
		return true, nil
	}
	before, err := s.ws.CurrentView()
	if err != nil {
		return false, err
	}
	after, err := restoreNavigationPoint(s, target)
	if err != nil {
		return false, err
	}
	if before.ID != after.ID || before.Name != after.Name {
		if err := s.ws.PrintCurrentStatus(s.stdout, s.stderr); err != nil {
			return false, err
		}
	}
	s.history.index = nextIndex
	return true, nil
}

func (s *DownloadSession) showNavigationStack() (bool, error) {
	if s == nil {
		return true, nil
	}
	return true, s.history.WriteTo(s.stderr)
}

func (s *navigationStack) WriteTo(w io.Writer) error {
	if s == nil || w == nil {
		return nil
	}
	for i, point := range s.points {
		marker := '-'
		if i == s.index {
			marker = '*'
		}
		if _, err := fmt.Fprintf(w, "%c  %s:%s\n", marker, point.name, point.addressString()); err != nil {
			return err
		}
	}
	return nil
}

func (p navigationPoint) addressString() string {
	if p.dotStart == p.dotEnd {
		return fmt.Sprintf("#%d", p.dotStart)
	}
	return fmt.Sprintf("#%d,#%d", p.dotStart, p.dotEnd)
}

func restoreNavigationPoint(s *DownloadSession, point navigationPoint) (wire.BufferView, error) {
	var view wire.BufferView
	var err error
	if point.fileID != 0 {
		view, err = s.ws.FocusFile(point.fileID)
		if err == nil {
			if view.DotStart == point.dotStart && view.DotEnd == point.dotEnd {
				return view, nil
			}
			return s.ws.SetDot(point.dotStart, point.dotEnd)
		}
	}
	if point.name == "" {
		return wire.BufferView{}, err
	}
	view, err = s.ws.OpenFiles([]string{point.name}, s.stdout, s.stderr)
	if err != nil {
		return wire.BufferView{}, err
	}
	if view.DotStart == point.dotStart && view.DotEnd == point.dotEnd {
		return view, nil
	}
	return s.ws.SetDot(point.dotStart, point.dotEnd)
}
