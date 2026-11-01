package ui

// scroller is a vertical window over pre-rendered lines. The SQL and log
// panes use it instead of bubbles/viewport so that pane keys are handled in
// one place and viewport's default bindings (d, u, …) can't clash with the
// global keys.
type scroller struct {
	lines  []string
	offset int
	height int
}

func (s *scroller) setLines(lines []string) {
	s.lines = lines
	s.clamp()
}

func (s *scroller) setHeight(h int) {
	s.height = max(h, 1)
	s.clamp()
}

func (s *scroller) maxOffset() int { return max(len(s.lines)-s.height, 0) }

func (s *scroller) clamp() { s.offset = min(max(s.offset, 0), s.maxOffset()) }

func (s *scroller) scroll(n int) {
	s.offset += n
	s.clamp()
}

func (s *scroller) top()    { s.offset = 0 }
func (s *scroller) bottom() { s.offset = s.maxOffset() }

func (s *scroller) visible() []string {
	end := min(s.offset+s.height, len(s.lines))
	if s.offset >= end {
		return nil
	}
	return s.lines[s.offset:end]
}
