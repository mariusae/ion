package term

type renderScheduler struct {
	pending   bool
	class     redrawClass
	forceFull bool
}

func (s *renderScheduler) Request(class redrawClass, forceFull bool) {
	if s == nil {
		return
	}
	s.pending = true
	s.class = class
	s.forceFull = s.forceFull || forceFull
}

func (s *renderScheduler) Pending() bool {
	return s != nil && s.pending
}

func (s *renderScheduler) Drain() (redrawClass, bool, bool) {
	if s == nil || !s.pending {
		return "", false, false
	}
	class := s.class
	forceFull := s.forceFull
	s.pending = false
	s.class = ""
	s.forceFull = false
	return class, forceFull, true
}
