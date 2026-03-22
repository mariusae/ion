package regexp

import (
	"fmt"
	"unicode"

	"ion/internal/core/text"
)

const (
	nsubexp = 10
	nprog   = 1024
	nlist   = 127

	operator = 0x1000000
	startTok = operator + 0
	rbraTok  = operator + 1
	lbraTok  = operator + 2
	orTok    = operator + 3
	catTok   = operator + 4
	starTok  = operator + 5
	plusTok  = operator + 6
	questTok = operator + 7

	anyTok     = 0x2000000
	nopTok     = anyTok + 1
	bolTok     = anyTok + 2
	eolTok     = anyTok + 3
	cclassTok  = anyTok + 4
	ncclassTok = anyTok + 5
	endTok     = anyTok + 0x77

	quoted = 0x4000000

	dclass = 10
	nstack = 20
)

type inst struct {
	typ   int
	subid int
	class int
	left  *inst
	right *inst
}

type node struct {
	first *inst
	last  *inst
}

// RangeSet matches sam's NSUBEXP capture structure.
type RangeSet struct {
	P [nsubexp]text.Range
}

type ilistEntry struct {
	inst   *inst
	ranges RangeSet
	startp text.Posn
}

// Pattern is a compiled sam structural regular expression.
type Pattern struct {
	program  []*inst
	start    *inst
	backward *inst
	classes  [][]rune
}

type compiler struct {
	program     []*inst
	progp       int
	start       *inst
	backward    *inst
	andstack    [nstack]node
	andp        int
	atorstack   [nstack]int
	atorp       int
	subidstack  [nstack]int
	subidp      int
	lastwasand  bool
	cursubid    int
	backwards   bool
	nbra        int
	expr        []rune
	pos         int
	classes     [][]rune
	negateClass bool
}

// Compile parses and compiles one sam regexp.
func Compile(s *text.String) (*Pattern, error) {
	c := &compiler{
		program: make([]*inst, 0, nprog),
	}

	start, err := c.realCompile(s.Runes(), false)
	if err != nil {
		return nil, err
	}
	c.optimize(0)
	backwardStartIndex := len(c.program)
	backStart, err := c.realCompile(s.Runes(), true)
	if err != nil {
		return nil, err
	}
	c.optimize(backwardStartIndex)

	return &Pattern{
		program:  c.program,
		start:    start,
		backward: backStart,
		classes:  c.classes,
	}, nil
}

func (c *compiler) realCompile(expr []rune, backwards bool) (*inst, error) {
	c.backwards = backwards
	c.expr = expr
	c.pos = 0
	c.atorp = 0
	c.andp = 0
	c.subidp = 0
	c.cursubid = 0
	c.lastwasand = false
	c.nbra = 0

	if err := c.pushator(startTok - 1); err != nil {
		return nil, err
	}
	for {
		token, err := c.lex()
		if err != nil {
			return nil, err
		}
		if token == endTok {
			break
		}
		if token&operator == operator {
			if err := c.operator(token); err != nil {
				return nil, err
			}
		} else {
			if err := c.operand(token); err != nil {
				return nil, err
			}
		}
	}
	if err := c.evalUntil(startTok); err != nil {
		return nil, err
	}
	if err := c.operand(endTok); err != nil {
		return nil, err
	}
	if err := c.evalUntil(startTok); err != nil {
		return nil, err
	}
	if c.nbra != 0 {
		return nil, fmt.Errorf("unmatched `('")
	}
	c.andp--
	return c.andstack[c.andp].first, nil
}

func (c *compiler) newInst(t int) (*inst, error) {
	if len(c.program) >= nprog {
		return nil, fmt.Errorf("reg. exp. list overflow")
	}
	i := &inst{typ: t, subid: -1, class: -1}
	c.program = append(c.program, i)
	return i, nil
}

func (c *compiler) operand(t int) error {
	if c.lastwasand {
		if err := c.operator(catTok); err != nil {
			return err
		}
	}
	i, err := c.newInst(t)
	if err != nil {
		return err
	}
	if t == cclassTok {
		if c.negateClass {
			i.typ = ncclassTok
		}
		i.class = len(c.classes) - 1
	}
	if err := c.pushand(i, i); err != nil {
		return err
	}
	c.lastwasand = true
	return nil
}

func (c *compiler) operator(t int) error {
	if t == rbraTok {
		c.nbra--
		if c.nbra < 0 {
			return fmt.Errorf("unmatched `)'")
		}
	}
	if t == lbraTok {
		c.cursubid++
		c.nbra++
		if c.lastwasand {
			if err := c.operator(catTok); err != nil {
				return err
			}
		}
	} else if err := c.evalUntil(t); err != nil {
		return err
	}
	if t != rbraTok {
		if err := c.pushator(t); err != nil {
			return err
		}
	}
	c.lastwasand = false
	if t == starTok || t == questTok || t == plusTok || t == rbraTok {
		c.lastwasand = true
	}
	return nil
}

func (c *compiler) pushand(first, last *inst) error {
	if c.andp >= len(c.andstack) {
		return fmt.Errorf("reg. exp. list overflow")
	}
	c.andstack[c.andp] = node{first: first, last: last}
	c.andp++
	return nil
}

func (c *compiler) pushator(t int) error {
	if c.atorp >= len(c.atorstack) {
		return fmt.Errorf("reg. exp. list overflow")
	}
	c.atorstack[c.atorp] = t
	c.atorp++
	if c.cursubid >= nsubexp {
		c.subidstack[c.subidp] = -1
	} else {
		c.subidstack[c.subidp] = c.cursubid
	}
	c.subidp++
	return nil
}

func (c *compiler) popand(op int) (*node, error) {
	if c.andp <= 0 {
		if op != 0 {
			return nil, fmt.Errorf("no operand for `%c'", rune(op))
		}
		return nil, fmt.Errorf("malformed regexp")
	}
	c.andp--
	return &c.andstack[c.andp], nil
}

func (c *compiler) popator() (int, int, error) {
	if c.atorp <= 0 || c.subidp <= 0 {
		return 0, 0, fmt.Errorf("reg. exp. list overflow")
	}
	c.atorp--
	c.subidp--
	return c.atorstack[c.atorp], c.subidstack[c.subidp], nil
}

func (c *compiler) evalUntil(pri int) error {
	for c.atorp > 0 && (pri == rbraTok || c.atorstack[c.atorp-1] >= pri) {
		op, subid, err := c.popator()
		if err != nil {
			return err
		}
		switch op {
		case lbraTok:
			op1, err := c.popand(int('('))
			if err != nil {
				return err
			}
			inst2, err := c.newInst(rbraTok)
			if err != nil {
				return err
			}
			inst2.subid = subid
			op1.last.left = inst2
			inst1, err := c.newInst(lbraTok)
			if err != nil {
				return err
			}
			inst1.subid = subid
			inst1.left = op1.first
			if err := c.pushand(inst1, inst2); err != nil {
				return err
			}
			return nil

		case orTok:
			op2, err := c.popand(int('|'))
			if err != nil {
				return err
			}
			op1, err := c.popand(int('|'))
			if err != nil {
				return err
			}
			inst2, err := c.newInst(nopTok)
			if err != nil {
				return err
			}
			op2.last.left = inst2
			op1.last.left = inst2
			inst1, err := c.newInst(orTok)
			if err != nil {
				return err
			}
			inst1.right = op1.first
			inst1.left = op2.first
			if err := c.pushand(inst1, inst2); err != nil {
				return err
			}

		case catTok:
			op2, err := c.popand(0)
			if err != nil {
				return err
			}
			op1, err := c.popand(0)
			if err != nil {
				return err
			}
			if c.backwards && op2.first.typ != endTok {
				op1, op2 = op2, op1
			}
			op1.last.left = op2.first
			if err := c.pushand(op1.first, op2.last); err != nil {
				return err
			}

		case starTok:
			op2, err := c.popand(int('*'))
			if err != nil {
				return err
			}
			inst1, err := c.newInst(orTok)
			if err != nil {
				return err
			}
			op2.last.left = inst1
			inst1.right = op2.first
			if err := c.pushand(inst1, inst1); err != nil {
				return err
			}

		case plusTok:
			op2, err := c.popand(int('+'))
			if err != nil {
				return err
			}
			inst1, err := c.newInst(orTok)
			if err != nil {
				return err
			}
			op2.last.left = inst1
			inst1.right = op2.first
			if err := c.pushand(op2.first, inst1); err != nil {
				return err
			}

		case questTok:
			op2, err := c.popand(int('?'))
			if err != nil {
				return err
			}
			inst1, err := c.newInst(orTok)
			if err != nil {
				return err
			}
			inst2, err := c.newInst(nopTok)
			if err != nil {
				return err
			}
			inst1.left = inst2
			inst1.right = op2.first
			op2.last.left = inst2
			if err := c.pushand(inst1, inst2); err != nil {
				return err
			}

		default:
			return fmt.Errorf("unknown regexp operator %d", op)
		}
	}
	return nil
}

func (c *compiler) optimize(startIndex int) {
	for i := startIndex; i < len(c.program); i++ {
		inst := c.program[i]
		if inst.typ == endTok {
			return
		}
		target := inst.left
		for target != nil && target.typ == nopTok {
			target = target.left
		}
		inst.left = target
	}
}

func (c *compiler) lex() (int, error) {
	if c.pos >= len(c.expr) {
		return endTok, nil
	}
	ch := c.expr[c.pos]
	c.pos++

	switch ch {
	case '\\':
		if c.pos < len(c.expr) {
			ch = c.expr[c.pos]
			c.pos++
			if ch == 'n' {
				ch = '\n'
			}
		}
		return int(ch), nil
	case 0:
		if c.pos > 0 {
			c.pos--
		}
		return endTok, nil
	case '*':
		return starTok, nil
	case '?':
		return questTok, nil
	case '+':
		return plusTok, nil
	case '|':
		return orTok, nil
	case '.':
		return anyTok, nil
	case '(':
		return lbraTok, nil
	case ')':
		return rbraTok, nil
	case '^':
		return bolTok, nil
	case '$':
		return eolTok, nil
	case '[':
		if err := c.buildClass(); err != nil {
			return 0, err
		}
		return cclassTok, nil
	default:
		return int(ch), nil
	}
}

func (c *compiler) nextRec() (int, error) {
	if c.pos >= len(c.expr) || (c.expr[c.pos] == '\\' && c.pos+1 >= len(c.expr)) {
		return 0, fmt.Errorf("malformed `[]'")
	}
	if c.expr[c.pos] == '\\' {
		c.pos++
		if c.expr[c.pos] == 'n' {
			c.pos++
			return '\n', nil
		}
		ch := int(c.expr[c.pos]) | quoted
		c.pos++
		return ch, nil
	}
	ch := int(c.expr[c.pos])
	c.pos++
	return ch, nil
}

func (c *compiler) buildClass() error {
	class := make([]rune, 0, dclass)
	if c.pos < len(c.expr) && c.expr[c.pos] == '^' {
		class = append(class, '\n')
		c.negateClass = true
		c.pos++
	} else {
		c.negateClass = false
	}
	for {
		c1, err := c.nextRec()
		if err != nil {
			return err
		}
		if c1 == int(']') {
			break
		}
		if c1 == int('-') {
			return fmt.Errorf("malformed `[]'")
		}
		if c.pos < len(c.expr) && c.expr[c.pos] == '-' {
			c.pos++
			c2, err := c.nextRec()
			if err != nil {
				return err
			}
			if c2 == int(']') {
				return fmt.Errorf("malformed `[]'")
			}
			class = append(class, rune(unicode.MaxRune), rune(c1&^quoted), rune(c2&^quoted))
		} else {
			class = append(class, rune(c1&^quoted))
		}
	}
	class = append(class, 0)
	c.classes = append(c.classes, class)
	return nil
}

func classMatch(class []rune, ch rune, negate bool) bool {
	for i := 0; i < len(class) && class[i] != 0; {
		if class[i] == rune(unicode.MaxRune) {
			if class[i+1] <= ch && ch <= class[i+2] {
				return !negate
			}
			i += 3
			continue
		}
		if class[i] == ch {
			return !negate
		}
		i++
	}
	return negate
}

// Execute runs the compiled regexp forward.
func (p *Pattern) Execute(f *text.File, startp, eof text.Posn) (RangeSet, bool, error) {
	var sel RangeSet
	sel.P[0].P1 = -1
	var sempty RangeSet
	listA := make([]ilistEntry, nlist+1)
	listB := make([]ilistEntry, nlist+1)
	flag := 0
	pos := startp
	nnl := 0
	wrapped := 0
	startchar := 0
	if p.start != nil && p.start.typ < operator {
		startchar = p.start.typ
	}

	for {
	doloop:
		c, err := f.ReadRune(pos)
		if err != nil {
			return sel, false, err
		}
		if pos >= eof || c < 0 {
			switch wrapped {
			case 0, 2:
				wrapped++
			case 1:
				wrapped++
				if sel.P[0].P1 >= 0 || eof != maxPosn() {
					return sel, sel.P[0].P1 >= 0, nil
				}
				clearList(listA)
				clearList(listB)
				pos = 0
				goto doloop
			default:
				return sel, sel.P[0].P1 >= 0, nil
			}
		} else if (((wrapped > 0) && pos >= startp) || sel.P[0].P1 > 0) && nnl == 0 {
			break
		}
		if startchar != 0 && nnl == 0 && int(c) != startchar {
			pos++
			continue
		}

		tl := listA
		nl := listB
		if flag != 0 {
			tl, nl = nl, tl
		}
		flag ^= 1
		clearList(nl)
		ntl := nnl
		nnl = 0
		if sel.P[0].P1 < 0 && (wrapped == 0 || pos < startp || startp == eof) {
			sempty.P[0].P1 = pos
			if addInst(tl, p.start, sempty) {
				ntl++
				if ntl >= nlist {
					return sel, false, fmt.Errorf("reg. exp. list overflow")
				}
			}
		}

		for i := 0; i < len(tl) && tl[i].inst != nil; i++ {
			inst := tl[i].inst
		switchstmt:
			switch inst.typ {
			default:
				if rune(inst.typ) == c {
					if addInst(nl, inst.left, tl[i].ranges) {
						nnl++
						if nnl >= nlist {
							return sel, false, fmt.Errorf("reg. exp. list overflow")
						}
					}
				}
			case lbraTok:
				if inst.subid >= 0 {
					tl[i].ranges.P[inst.subid].P1 = pos
				}
				inst = inst.left
				goto switchstmt
			case rbraTok:
				if inst.subid >= 0 {
					tl[i].ranges.P[inst.subid].P2 = pos
				}
				inst = inst.left
				goto switchstmt
			case anyTok:
				if c != '\n' && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case bolTok:
				prev, err := f.ReadRune(pos - 1)
				if err != nil {
					return sel, false, err
				}
				if pos == 0 || prev == '\n' {
					inst = inst.left
					goto switchstmt
				}
			case eolTok:
				if c == '\n' {
					inst = inst.left
					goto switchstmt
				}
			case cclassTok:
				if c >= 0 && classMatch(p.classes[inst.class], c, false) && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case ncclassTok:
				if c >= 0 && classMatch(p.classes[inst.class], c, true) && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case orTok:
				if addInst(tl, inst.right, tl[i].ranges) {
					ntl++
					if ntl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
				inst = inst.left
				goto switchstmt
			case endTok:
				m := tl[i].ranges
				m.P[0].P2 = pos
				newMatch(&sel, &m)
			}
		}
		pos++
	}
	return sel, sel.P[0].P1 >= 0, nil
}

// BExecute runs the compiled regexp backward.
func (p *Pattern) BExecute(f *text.File, startp text.Posn) (RangeSet, bool, error) {
	var sel RangeSet
	sel.P[0].P1 = -1
	var sempty RangeSet
	listA := make([]ilistEntry, nlist+1)
	listB := make([]ilistEntry, nlist+1)
	flag := 0
	pos := startp
	nnl := 0
	wrapped := 0
	startchar := 0
	if p.backward != nil && p.backward.typ < operator {
		startchar = p.backward.typ
	}

	for {
	doloop:
		c, err := f.ReadRune(pos - 1)
		if err != nil {
			return sel, false, err
		}
		if c < 0 {
			switch wrapped {
			case 0, 2:
				wrapped++
			case 1:
				wrapped++
				if sel.P[0].P1 >= 0 {
					return sel, true, nil
				}
				clearList(listA)
				clearList(listB)
				pos = text.Posn(f.B.Len())
				goto doloop
			default:
				return sel, sel.P[0].P1 >= 0, nil
			}
		} else if (((wrapped > 0) && pos <= startp) || sel.P[0].P1 > 0) && nnl == 0 {
			break
		}
		if startchar != 0 && nnl == 0 && int(c) != startchar {
			pos--
			continue
		}

		tl := listA
		nl := listB
		if flag != 0 {
			tl, nl = nl, tl
		}
		flag ^= 1
		clearList(nl)
		ntl := nnl
		nnl = 0
		if sel.P[0].P1 < 0 && (wrapped == 0 || pos > startp) {
			sempty.P[0].P1 = -pos
			if addInst(tl, p.backward, sempty) {
				ntl++
				if ntl >= nlist {
					return sel, false, fmt.Errorf("reg. exp. list overflow")
				}
			}
		}

		for i := 0; i < len(tl) && tl[i].inst != nil; i++ {
			inst := tl[i].inst
		switchstmt:
			switch inst.typ {
			default:
				if rune(inst.typ) == c {
					if addInst(nl, inst.left, tl[i].ranges) {
						nnl++
						if nnl >= nlist {
							return sel, false, fmt.Errorf("reg. exp. list overflow")
						}
					}
				}
			case lbraTok:
				if inst.subid >= 0 {
					tl[i].ranges.P[inst.subid].P1 = pos
				}
				inst = inst.left
				goto switchstmt
			case rbraTok:
				if inst.subid >= 0 {
					tl[i].ranges.P[inst.subid].P2 = pos
				}
				inst = inst.left
				goto switchstmt
			case anyTok:
				if c != '\n' && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case bolTok:
				if c == '\n' || pos == 0 {
					inst = inst.left
					goto switchstmt
				}
			case eolTok:
				next, err := f.ReadRune(pos)
				if err != nil {
					return sel, false, err
				}
				if pos == text.Posn(f.B.Len()) || next == '\n' {
					inst = inst.left
					goto switchstmt
				}
			case cclassTok:
				if c >= 0 && classMatch(p.classes[inst.class], c, false) && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case ncclassTok:
				if c >= 0 && classMatch(p.classes[inst.class], c, true) && addInst(nl, inst.left, tl[i].ranges) {
					nnl++
					if nnl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
			case orTok:
				if addInst(tl, inst.right, tl[i].ranges) {
					ntl++
					if ntl >= nlist {
						return sel, false, fmt.Errorf("reg. exp. list overflow")
					}
				}
				inst = inst.left
				goto switchstmt
			case endTok:
				m := tl[i].ranges
				m.P[0].P1 = -m.P[0].P1
				m.P[0].P2 = pos
				backwardMatch(&sel, &m)
			}
		}
		pos--
	}
	return sel, sel.P[0].P1 >= 0, nil
}

func addInst(list []ilistEntry, inst *inst, ranges RangeSet) bool {
	if inst == nil {
		return false
	}
	for i := 0; i < len(list) && list[i].inst != nil; i++ {
		if list[i].inst == inst {
			if ranges.P[0].P1 < list[i].ranges.P[0].P1 {
				list[i].ranges = ranges
			}
			return false
		}
	}
	for i := 0; i < len(list); i++ {
		if list[i].inst == nil {
			list[i] = ilistEntry{inst: inst, ranges: ranges}
			if i+1 < len(list) {
				list[i+1].inst = nil
			}
			return true
		}
	}
	return false
}

func clearList(list []ilistEntry) {
	for i := range list {
		list[i].inst = nil
	}
}

func newMatch(sel *RangeSet, cand *RangeSet) {
	if sel.P[0].P1 < 0 ||
		cand.P[0].P1 < sel.P[0].P1 ||
		(cand.P[0].P1 == sel.P[0].P1 && cand.P[0].P2 > sel.P[0].P2) {
		*sel = *cand
	}
}

func backwardMatch(sel *RangeSet, cand *RangeSet) {
	if sel.P[0].P1 < 0 ||
		cand.P[0].P1 > sel.P[0].P2 ||
		(cand.P[0].P1 == sel.P[0].P2 && cand.P[0].P2 < sel.P[0].P1) {
		for i := range sel.P {
			sel.P[i].P1 = cand.P[i].P2
			sel.P[i].P2 = cand.P[i].P1
		}
	}
}

func maxPosn() text.Posn {
	return text.Posn(1<<62 - 1)
}
