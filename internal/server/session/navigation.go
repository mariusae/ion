package session

import (
	"fmt"
	"io"
	"strings"

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

func sameNavigationFile(a, b navigationPoint) bool {
	if a.fileID != 0 && b.fileID != 0 {
		return a.fileID == b.fileID
	}
	return a.name == b.name
}

func sameNavigationPoint(a, b navigationPoint) bool {
	return sameNavigationFile(a, b) && a.dotStart == b.dotStart && a.dotEnd == b.dotEnd
}

func (s *navigationStack) normalize() {
	if s == nil {
		return
	}
	if len(s.points) == 0 {
		s.index = -1
		return
	}
	if s.index < 0 {
		s.index = 0
	}
	if s.index >= len(s.points) {
		s.index = len(s.points) - 1
	}
}

func (s *navigationStack) PushTransition(source, dest navigationPoint) {
	if s == nil {
		return
	}
	s.normalize()
	if len(s.points) == 0 {
		s.points = append(s.points, source)
		s.index = 0
	} else {
		s.points = s.points[:s.index+1]
		if !sameNavigationPoint(s.points[s.index], source) {
			s.points = append(s.points, source)
			s.index = len(s.points) - 1
		}
	}
	if sameNavigationPoint(s.points[s.index], dest) {
		return
	}
	s.points = append(s.points[:s.index+1], dest)
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

func (s *DownloadSession) pushHistory(source, dest wire.BufferView) {
	if s == nil {
		return
	}
	src, ok := navigationPointFromView(source)
	if !ok {
		return
	}
	dst, ok := navigationPointFromView(dest)
	if !ok {
		return
	}
	s.history.PushTransition(src, dst)
}

func (s *DownloadSession) navigate(delta int) (bool, error) {
	s.history.normalize()
	target, nextIndex, ok := s.history.Target(delta)
	if !ok {
		return true, nil
	}
	_, err := restoreNavigationPoint(s, target)
	if err != nil {
		return false, err
	}
	s.history.index = nextIndex
	return true, nil
}

func (s *DownloadSession) popNavigation() (bool, error) {
	if s == nil {
		return true, nil
	}
	s.history.normalize()
	if s.history.index <= 0 || s.history.index >= len(s.history.points) {
		return true, nil
	}
	ok, err := s.navigate(-1)
	if err != nil || !ok {
		return ok, err
	}
	s.history.points = s.history.points[:s.history.index+1]
	return true, nil
}

func (s *DownloadSession) showNavigationStack() (bool, error) {
	if s == nil {
		return true, nil
	}
	if s.stderr == nil {
		return true, nil
	}
	menuFiles, err := s.ws.MenuFiles(s.state)
	if err != nil {
		return true, err
	}
	currentDirty := false
	currentChanged := false
	currentPoint := navigationPoint{}
	hasCurrentPoint := false
	for _, file := range menuFiles {
		if !file.Current {
			continue
		}
		currentDirty = file.Dirty
		currentChanged = file.Changed
		if view, err := s.ws.CurrentView(s.state); err == nil {
			if point, ok := navigationPointFromView(view); ok {
				currentPoint = point
				hasCurrentPoint = true
			}
		}
		break
	}
	for i, point := range s.history.points {
		if i == s.history.index {
			mod := ' '
			if currentDirty && (!hasCurrentPoint || sameNavigationFile(point, currentPoint)) {
				mod = dirtyMenuMark(currentDirty, currentChanged)
			}
			if _, err := fmt.Fprintf(s.stderr, "%c-. %s\n", mod, point.displayLabel()); err != nil {
				return true, err
			}
			continue
		}
		if _, err := fmt.Fprintf(s.stderr, " -  %s\n", point.displayLabel()); err != nil {
			return true, err
		}
	}
	return true, nil
}

func dirtyMenuMark(dirty, changed bool) rune {
	if dirty && changed {
		return '"'
	}
	if dirty {
		return '\''
	}
	return ' '
}

func (p navigationPoint) displayLabel() string {
	return p.name + ":" + p.addressString()
}

func (p navigationPoint) addressString() string {
	if p.dotStart == p.dotEnd {
		return fmt.Sprintf("#%d", p.dotStart)
	}
	return fmt.Sprintf("#%d,#%d", p.dotStart, p.dotEnd)
}

func (s *DownloadSession) navigationStack() wire.NavigationStack {
	if s == nil {
		return wire.NavigationStack{Current: -1}
	}
	s.history.normalize()
	entries := make([]wire.NavigationEntry, 0, len(s.history.points))
	for _, point := range s.history.points {
		entries = append(entries, wire.NavigationEntry{Label: point.displayLabel()})
	}
	return wire.NavigationStack{
		Entries: entries,
		Current: s.history.index,
	}
}

func restoreNavigationPoint(s *DownloadSession, point navigationPoint) (wire.BufferView, error) {
	var view wire.BufferView
	var err error
	if point.fileID != 0 {
		view, err = s.ws.FocusFile(s.state, point.fileID)
		if err == nil {
			if view.DotStart == point.dotStart && view.DotEnd == point.dotEnd {
				return view, nil
			}
			return s.ws.SetDot(s.state, point.dotStart, point.dotEnd)
		}
	}
	if point.name == "" {
		return wire.BufferView{}, err
	}
	view, err = s.ws.OpenFiles(s.state, []string{point.name}, io.Discard, io.Discard)
	if err != nil {
		return wire.BufferView{}, err
	}
	if view.DotStart == point.dotStart && view.DotEnd == point.dotEnd {
		return view, nil
	}
	return s.ws.SetDot(s.state, point.dotStart, point.dotEnd)
}
