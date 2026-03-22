package cmdlang

import (
	"errors"
	"fmt"
	"strings"

	"ion/internal/core/addr"
	"ion/internal/core/text"
)

type defAddr int

const (
	aNo defAddr = iota
	aDot
	aAll
)

type cmdSpec struct {
	cmdc    rune
	text    bool
	regexp  bool
	addrArg bool
	defcmd  rune
	defaddr defAddr
	count   int
	token   string
}

var cmdTable = []cmdSpec{
	{'\n', false, false, false, 0, aDot, 0, ""},
	{'a', true, false, false, 0, aDot, 0, ""},
	{'b', false, false, false, 0, aNo, 0, "\n"},
	{'B', false, false, false, 0, aNo, 0, "\n"},
	{'c', true, false, false, 0, aDot, 0, ""},
	{'d', false, false, false, 0, aDot, 0, ""},
	{'D', false, false, false, 0, aNo, 0, "\n"},
	{'e', false, false, false, 0, aNo, 0, " \t\n"},
	{'f', false, false, false, 0, aNo, 0, " \t\n"},
	{'g', false, true, false, 'p', aDot, 0, ""},
	{'i', true, false, false, 0, aDot, 0, ""},
	{'k', false, false, false, 0, aDot, 0, ""},
	{'m', false, false, true, 0, aDot, 0, ""},
	{'n', false, false, false, 0, aNo, 0, ""},
	{'p', false, false, false, 0, aDot, 0, ""},
	{'q', false, false, false, 0, aNo, 0, ""},
	{'r', false, false, false, 0, aDot, 0, " \t\n"},
	{'s', false, true, false, 0, aDot, 1, ""},
	{'t', false, false, true, 0, aDot, 0, ""},
	{'u', false, false, false, 0, aNo, 2, ""},
	{'v', false, true, false, 'p', aDot, 0, ""},
	{'w', false, false, false, 0, aAll, 0, " \t\n"},
	{'x', false, true, false, 'p', aDot, 0, ""},
	{'y', false, true, false, 'p', aDot, 0, ""},
	{'X', false, true, false, 'f', aNo, 0, ""},
	{'Y', false, true, false, 'f', aNo, 0, ""},
	{'!', false, false, false, 0, aNo, 0, "\n"},
	{'>', false, false, false, 0, aDot, 0, "\n"},
	{'<', false, false, false, 0, aDot, 0, "\n"},
	{'|', false, false, false, 0, aDot, 0, "\n"},
	{'=', false, false, false, 0, aDot, 0, "\n"},
	{'c' | 0x100, false, false, false, 0, aNo, 0, " \t\n"},
}

var errBlockEnd = errors.New("block end")

// ErrNeedMoreInput reports that parsing stopped only because more input is needed.
var ErrNeedMoreInput = errors.New("need more input")

// Cmd is the parsed sam command AST.
type Cmd struct {
	Addr    *addr.Addr
	Re      *text.String
	Cmd     *Cmd
	Text    *text.String
	AddrArg *addr.Addr
	Next    *Cmd
	Num     int
	Flag    rune
	Cmdc    rune
}

// Parser parses sam command language.
type Parser struct {
	input   []rune
	pos     int
	final   bool
	last    int
	lastPat text.String
}

// NewParser constructs a parser for one command script.
func NewParser(src string) *Parser {
	return NewParserRunes([]rune(src))
}

// NewParserRunes constructs a parser over an existing rune buffer.
func NewParserRunes(src []rune) *Parser {
	return &Parser{
		input:   src,
		last:    -1,
		lastPat: text.NewString(),
	}
}

// ResetRunes replaces the parser input while preserving cross-command state.
func (p *Parser) ResetRunes(src []rune) {
	p.input = src
	p.pos = 0
	p.final = true
	p.last = -1
}

// Parse parses one command from the current position.
func (p *Parser) Parse() (*Cmd, error) {
	return p.ParseWithFinal(true)
}

// ParseWithFinal parses one command, treating EOF as final only when final is true.
func (p *Parser) ParseWithFinal(final bool) (*Cmd, error) {
	p.final = final
	cmd, err := p.parseCmd(0)
	if errors.Is(err, errBlockEnd) {
		return nil, fmt.Errorf("unexpected }")
	}
	return cmd, err
}

// Consumed reports how many runes were consumed by the last parse.
func (p *Parser) Consumed() int {
	if p.pos < 0 {
		return 0
	}
	if p.pos > len(p.input) {
		return len(p.input)
	}
	return p.pos
}

func (p *Parser) parseCmd(nest int) (*Cmd, error) {
	cmd := Cmd{}
	var err error
	cmd.Addr, err = p.compoundAddr()
	if err != nil {
		return nil, err
	}
	if p.skipBl() == -1 {
		if !p.final && p.pos > 0 {
			return nil, ErrNeedMoreInput
		}
		return nil, nil
	}
	c := p.getch()
	if c == -1 {
		return nil, nil
	}
	cmd.Cmdc = rune(c)
	if cmd.Cmdc == 'c' && p.nextc() == int('d') {
		p.getch()
		cmd.Cmdc = 'c' | 0x100
	}

	if spec, ok := lookup(cmd.Cmdc); ok {
		if cmd.Cmdc == '\n' {
			return &cmd, nil
		}
		if spec.defaddr == aNo && cmd.Addr != nil {
			return nil, fmt.Errorf("command takes no address")
		}
		if spec.count > 0 {
			cmd.Num = int(p.getnum(spec.count))
		}
		if spec.regexp {
			if (spec.cmdc != 'x' && spec.cmdc != 'X') || !isBlankOrNL(p.nextc()) {
				p.skipBl()
				delim := p.getch()
				if delim == -1 || delim == '\n' {
					return nil, fmt.Errorf("pattern expected")
				}
				if err := okDelim(rune(delim)); err != nil {
					return nil, err
				}
				cmd.Re, err = p.getRegexp(rune(delim))
				if err != nil {
					return nil, err
				}
				if spec.cmdc == 's' {
					cmd.Text, err = p.newString()
					if err != nil {
						return nil, err
					}
					if err := p.getRHS(cmd.Text, rune(delim), 's'); err != nil {
						return nil, err
					}
					if p.nextc() == delim {
						p.getch()
						if p.nextc() == int('g') {
							cmd.Flag = rune(p.getch())
						}
					}
				}
			}
		}
		if spec.addrArg {
			cmd.AddrArg, err = p.simpleAddr()
			if err != nil {
				return nil, err
			}
			if cmd.AddrArg == nil {
				return nil, fmt.Errorf("missing address argument")
			}
		}
		if spec.defcmd != 0 {
			if p.skipBl() == int('\n') {
				p.getch()
				cmd.Cmd = &Cmd{Cmdc: spec.defcmd}
			} else {
				cmd.Cmd, err = p.parseCmd(nest)
				if err != nil {
					return nil, err
				}
			}
		} else if spec.text {
			cmd.Text, err = p.collectText()
			if err != nil {
				return nil, err
			}
		} else if spec.token != "" {
			cmd.Text, err = p.collectToken(spec.token)
			if err != nil {
				return nil, err
			}
		} else if err := p.atNL(); err != nil {
			return nil, err
		}
		return &cmd, nil
	}

	switch cmd.Cmdc {
	case '{':
		var head, tail *Cmd
		for {
			if p.skipBl() == int('\n') {
				p.getch()
			}
			next, err := p.parseCmd(nest + 1)
			if errors.Is(err, errBlockEnd) {
				break
			}
			if err != nil {
				return nil, err
			}
			if next == nil {
				break
			}
			if head == nil {
				head = next
			} else {
				tail.Next = next
			}
			tail = next
		}
		cmd.Cmd = head
		return &cmd, nil
	case '}':
		if err := p.atNL(); err != nil {
			return nil, err
		}
		if nest == 0 {
			return nil, fmt.Errorf("unmatched `}'")
		}
		return nil, errBlockEnd
	default:
		return nil, errorC("unknown command", cmd.Cmdc)
	}
}

func (p *Parser) simpleAddr() (*addr.Addr, error) {
	var a addr.Addr
	a.Num = 0

	switch p.skipBl() {
	case '#':
		a.Type = rune(p.getch())
		a.Num = p.getnum(1)
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		a.Num = p.getnum(1)
		a.Type = 'l'
	case '/', '?', '"':
		a.Type = rune(p.getch())
		re, err := p.getRegexp(a.Type)
		if err != nil {
			return nil, err
		}
		a.Re = re
	case '.', '$', '+', '-', '\'':
		a.Type = rune(p.getch())
	default:
		return nil, nil
	}

	next, err := p.simpleAddr()
	if err != nil {
		return nil, err
	}
	a.Next = next
	if a.Next != nil {
		switch a.Next.Type {
		case '.', '$', '\'':
			if a.Type != '"' {
				return nil, fmt.Errorf("bad address syntax")
			}
		case '"':
			if a.Type != '"' {
				return nil, fmt.Errorf("bad address syntax")
			}
		case 'l', '#':
			fallthrough
		case '/', '?':
			if a.Type != '+' && a.Type != '-' {
				inserted := &addr.Addr{Type: '+', Next: a.Next}
				a.Next = inserted
			}
		case '+', '-':
		default:
			return nil, fmt.Errorf("bad address syntax")
		}
	}
	return cloneAddr(&a), nil
}

func (p *Parser) compoundAddr() (*addr.Addr, error) {
	left, err := p.simpleAddr()
	if err != nil {
		return nil, err
	}
	sep := p.skipBl()
	if sep != int(',') && sep != int(';') {
		return left, nil
	}
	p.getch()
	next, err := p.compoundAddr()
	if err != nil {
		return nil, err
	}
	if next != nil && (next.Type == ',' || next.Type == ';') && next.Left == nil {
		return nil, fmt.Errorf("bad address syntax")
	}
	return &addr.Addr{
		Type: rune(sep),
		Left: left,
		Next: next,
	}, nil
}

func (p *Parser) collectToken(end string) (*text.String, error) {
	s, err := p.newString()
	if err != nil {
		return nil, err
	}
	for c := p.nextc(); c == int(' ') || c == int('\t'); c = p.nextc() {
		if err := s.Add(rune(p.getch())); err != nil {
			return nil, err
		}
	}
	for c := p.getch(); c > 0 && !strings.ContainsRune(end, rune(c)); c = p.getch() {
		if err := s.Add(rune(c)); err != nil {
			return nil, err
		}
	}
	if err := s.Add(0); err != nil {
		return nil, err
	}
	if p.lastRead() != '\n' {
		if err := p.atNL(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (p *Parser) collectText() (*text.String, error) {
	s, err := p.newString()
	if err != nil {
		return nil, err
	}
	if p.skipBl() == int('\n') {
		p.getch()
		for {
			beg := s.Len()
			for c := p.getch(); c > 0 && c != '\n'; c = p.getch() {
				if err := s.Add(rune(c)); err != nil {
					return nil, err
				}
			}
			if err := s.Add('\n'); err != nil {
				return nil, err
			}
			if beg+2 <= s.Len() && s.Runes()[beg] == '.' && s.Runes()[beg+1] == '\n' {
				if err := s.Delete(text.Posn(s.Len()-2), text.Posn(s.Len())); err != nil {
					return nil, err
				}
				break
			}
			if p.lastRead() < 0 {
				if !p.final {
					return nil, ErrNeedMoreInput
				}
				break
			}
		}
	} else {
		delim := rune(p.getch())
		if err := okDelim(delim); err != nil {
			return nil, err
		}
		if err := p.getRHS(s, delim, 'a'); err != nil {
			return nil, err
		}
		if p.nextc() == int(delim) {
			p.getch()
		}
		if err := p.atNL(); err != nil {
			return nil, err
		}
	}
	if err := s.Add(0); err != nil {
		return nil, err
	}
	return s, nil
}

func (p *Parser) getRegexp(delim rune) (*text.String, error) {
	gen := text.NewString()
	for {
		c := p.getch()
		if c < 0 && !p.final {
			return nil, ErrNeedMoreInput
		}
		if c == int('\\') {
			if p.nextc() == int(delim) {
				c = p.getch()
			} else if p.nextc() == int('\\') {
				if err := gen.Add(rune(c)); err != nil {
					return nil, err
				}
				c = p.getch()
			}
		}
		if c == int(delim) || c == int('\n') || c < 0 {
			break
		}
		if err := gen.Add(rune(c)); err != nil {
			return nil, err
		}
	}
	if lr := p.lastRead(); lr != int(delim) && lr > 0 {
		p.ungetch()
	}
	if gen.Len() > 0 {
		p.lastPat.Zero()
		if err := p.lastPat.DupString(&gen); err != nil {
			return nil, err
		}
		if err := p.lastPat.Add(0); err != nil {
			return nil, err
		}
	}
	if p.lastPat.Len() <= 1 {
		return nil, fmt.Errorf("pattern expected")
	}
	out := text.NewString()
	if err := out.DupString(&p.lastPat); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *Parser) getRHS(s *text.String, delim rune, cmd rune) error {
	for c := p.getch(); c > 0 && rune(c) != delim && c != int('\n'); c = p.getch() {
		if c == int('\\') {
			c = p.getch()
			if c <= 0 {
				if !p.final {
					return ErrNeedMoreInput
				}
				return fmt.Errorf("bad rhs")
			}
			if c == int('\n') {
				p.ungetch()
				c = int('\\')
			} else if c == int('n') {
				c = int('\n')
			} else if rune(c) != delim && (cmd == 's' || c != int('\\')) {
				if err := s.Add('\\'); err != nil {
					return err
				}
			}
		}
		if err := s.Add(rune(c)); err != nil {
			return err
		}
	}
	p.ungetch()
	return nil
}

func (p *Parser) atNL() error {
	p.skipBl()
	if ch := p.getch(); ch == -1 && !p.final {
		return ErrNeedMoreInput
	} else if ch != int('\n') {
		return fmt.Errorf("newline expected")
	}
	return nil
}

func (p *Parser) getnum(signok int) text.Posn {
	n := text.Posn(0)
	sign := text.Posn(1)
	if signok > 1 && p.nextc() == int('-') {
		sign = -1
		p.getch()
	}
	c := p.nextc()
	if c < int('0') || c > int('9') {
		return sign
	}
	for c = p.getch(); c >= int('0') && c <= int('9'); c = p.getch() {
		n = n*10 + text.Posn(c-int('0'))
	}
	p.ungetch()
	return sign * n
}

func (p *Parser) skipBl() int {
	c := p.getch()
	for c == int(' ') || c == int('\t') {
		c = p.getch()
	}
	if c >= 0 {
		p.ungetch()
	}
	return c
}

func (p *Parser) getch() int {
	if p.pos >= len(p.input) {
		p.last = -1
		return -1
	}
	c := p.input[p.pos]
	p.pos++
	p.last = int(c)
	return p.last
}

func (p *Parser) nextc() int {
	if p.pos >= len(p.input) {
		return -1
	}
	return int(p.input[p.pos])
}

func (p *Parser) ungetch() {
	if p.pos > 0 {
		p.pos--
	}
}

func (p *Parser) lastRead() int {
	return p.last
}

func (p *Parser) newString() (*text.String, error) {
	s := text.NewString()
	return &s, nil
}

func lookup(c rune) (cmdSpec, bool) {
	for _, spec := range cmdTable {
		if spec.cmdc == c {
			return spec, true
		}
	}
	return cmdSpec{}, false
}

func okDelim(c rune) error {
	if c == '\\' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
		return errorC("bad delimiter", c)
	}
	return nil
}

func errorC(msg string, c rune) error {
	return fmt.Errorf("%s `%c'", msg, c)
}

func isBlankOrNL(c int) bool {
	return c == int(' ') || c == int('\t') || c == int('\n')
}

func cloneAddr(a *addr.Addr) *addr.Addr {
	if a == nil {
		return nil
	}
	out := *a
	return &out
}
