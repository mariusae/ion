package addr

import (
	"fmt"
	"math"
	"strings"

	ionregexp "ion/internal/core/regexp"
	"ion/internal/core/text"
)

// Address is a resolved address within one file.
type Address struct {
	R text.Range
	F *text.File
}

// Addr is the parsed address AST shape from sam's command language.
type Addr struct {
	Type rune
	Re   *text.String
	Left *Addr
	Num  text.Posn
	Next *Addr
}

// Evaluator resolves address expressions against files.
type Evaluator struct {
	Files   []*text.File
	Current *text.File
}

// Resolve evaluates ap starting from a.
func (e *Evaluator) Resolve(ap *Addr, a Address, sign int) (Address, error) {
	f := a.F
	for ap != nil {
		switch ap.Type {
		case 'l', '#':
			var err error
			if ap.Type == '#' {
				a, err = CharAddr(ap.Num, a, sign)
			} else {
				a, err = LineAddr(ap.Num, a, sign)
			}
			if err != nil {
				return Address{}, err
			}

		case '.':
			a = Address{F: f, R: f.Dot}

		case '$':
			a.R.P1 = text.Posn(f.B.Len())
			a.R.P2 = a.R.P1

		case '\'':
			a.R = f.Mark

		case '?':
			sign = -sign
			if sign == 0 {
				sign = -1
			}
			fallthrough
		case '/':
			match, err := nextMatch(f, ap.Re, chooseSearchPos(a, sign), sign)
			if err != nil {
				return Address{}, err
			}
			a.R = match

		case '"':
			match, err := e.matchFile(ap.Re)
			if err != nil {
				return Address{}, err
			}
			a = Address{F: match, R: match.Dot}
			f = match

		case '*':
			a.R.P1 = 0
			a.R.P2 = text.Posn(f.B.Len())
			return a, nil

		case ',', ';':
			var a1, a2 Address
			var err error
			if ap.Left != nil {
				a1, err = e.Resolve(ap.Left, a, 0)
				if err != nil {
					return Address{}, err
				}
			} else {
				a1 = Address{F: a.F, R: text.Range{}}
			}
			if ap.Type == ';' {
				f = a1.F
				a = a1
				f.Dot = a1.R
			}
			if ap.Next != nil {
				a2, err = e.Resolve(ap.Next, a, 0)
				if err != nil {
					return Address{}, err
				}
			} else {
				a2 = Address{F: a.F, R: text.Range{P1: text.Posn(f.B.Len()), P2: text.Posn(f.B.Len())}}
			}
			if a1.F != a2.F {
				return Address{}, fmt.Errorf("address range spans different files")
			}
			a = Address{
				F: a1.F,
				R: text.Range{P1: a1.R.P1, P2: a2.R.P2},
			}
			if a.R.P2 < a.R.P1 {
				return Address{}, fmt.Errorf("address out of order")
			}
			return a, nil

		case '+', '-':
			sign = 1
			if ap.Type == '-' {
				sign = -1
			}
			if ap.Next == nil || ap.Next.Type == '+' || ap.Next.Type == '-' {
				var err error
				a, err = LineAddr(1, a, sign)
				if err != nil {
					return Address{}, err
				}
			}

		default:
			return Address{}, fmt.Errorf("unsupported address type %q", ap.Type)
		}
		ap = ap.Next
	}
	return a, nil
}

// CharAddr matches sam's `#` address behavior.
func CharAddr(n text.Posn, addr Address, sign int) (Address, error) {
	switch {
	case sign == 0:
		addr.R.P1, addr.R.P2 = n, n
	case sign < 0:
		addr.R.P1 -= n
		addr.R.P2 = addr.R.P1
	case sign > 0:
		addr.R.P2 += n
		addr.R.P1 = addr.R.P2
	}
	if addr.R.P1 < 0 || int(addr.R.P2) > addr.F.B.Len() {
		return Address{}, fmt.Errorf("address out of range")
	}
	return addr, nil
}

// LineAddr matches sam's `l` address behavior.
func LineAddr(n text.Posn, addr Address, sign int) (Address, error) {
	f := addr.F
	var a Address
	a.F = f

	if sign >= 0 {
		var p text.Posn
		var count text.Posn
		if n == 0 {
			if sign == 0 || addr.R.P2 == 0 {
				a.R.P1, a.R.P2 = 0, 0
				return a, nil
			}
			a.R.P1 = addr.R.P2
			p = addr.R.P2 - 1
		} else {
			if sign == 0 || addr.R.P2 == 0 {
				p = 0
				count = 1
			} else {
				p = addr.R.P2 - 1
				ch, err := f.ReadRune(p)
				if err != nil {
					return Address{}, err
				}
				p++
				if ch == '\n' {
					count = 1
				}
			}
			for count < n {
				if int(p) >= f.B.Len() {
					return Address{}, fmt.Errorf("address out of range")
				}
				ch, err := f.ReadRune(p)
				if err != nil {
					return Address{}, err
				}
				p++
				if ch == '\n' {
					count++
				}
			}
			a.R.P1 = p
		}
		for int(p) < f.B.Len() {
			ch, err := f.ReadRune(p)
			if err != nil {
				return Address{}, err
			}
			p++
			if ch == '\n' {
				break
			}
		}
		a.R.P2 = p
		return a, nil
	}

	p := addr.R.P1
	if n == 0 {
		a.R.P2 = addr.R.P1
	} else {
		var count text.Posn
		for count = 0; count < n; {
			if p == 0 {
				count++
				if count != n {
					return Address{}, fmt.Errorf("address out of range")
				}
			} else {
				ch, err := f.ReadRune(p - 1)
				if err != nil {
					return Address{}, err
				}
				if ch != '\n' || count+1 != n {
					p--
				}
				if ch == '\n' {
					count++
				}
			}
		}
		a.R.P2 = p
		if p > 0 {
			p--
		}
	}
	for p > 0 {
		ch, err := f.ReadRune(p - 1)
		if err != nil {
			return Address{}, err
		}
		if ch == '\n' {
			break
		}
		p--
	}
	a.R.P1 = p
	return a, nil
}

func nextMatch(f *text.File, re *text.String, p text.Posn, sign int) (text.Range, error) {
	pat, err := ionregexp.Compile(re)
	if err != nil {
		return text.Range{}, err
	}
	if sign >= 0 {
		match, ok, err := pat.Execute(f, p, maxPosn())
		if err != nil {
			return text.Range{}, err
		}
		if !ok {
			return text.Range{}, fmt.Errorf("search failed")
		}
		if match.P[0].P1 == match.P[0].P2 && match.P[0].P1 == p {
			p++
			if int(p) > f.B.Len() {
				p = 0
			}
			match, ok, err = pat.Execute(f, p, maxPosn())
			if err != nil {
				return text.Range{}, err
			}
			if !ok {
				return text.Range{}, fmt.Errorf("search failed")
			}
		}
		return match.P[0], nil
	}

	match, ok, err := pat.BExecute(f, p)
	if err != nil {
		return text.Range{}, err
	}
	if !ok {
		return text.Range{}, fmt.Errorf("search failed")
	}
	if match.P[0].P1 == match.P[0].P2 && match.P[0].P2 == p {
		p--
		if p < 0 {
			p = text.Posn(f.B.Len())
		}
		match, ok, err = pat.BExecute(f, p)
		if err != nil {
			return text.Range{}, err
		}
		if !ok {
			return text.Range{}, fmt.Errorf("search failed")
		}
	}
	return match.P[0], nil
}

func chooseSearchPos(a Address, sign int) text.Posn {
	if sign >= 0 {
		return a.R.P2
	}
	return a.R.P1
}

func (e *Evaluator) matchFile(re *text.String) (*text.File, error) {
	pat, err := ionregexp.Compile(re)
	if err != nil {
		return nil, err
	}
	var match *text.File
	for _, f := range e.Files {
		if f == nil {
			continue
		}
		tmpDisk, err := text.NewDisk()
		if err != nil {
			return nil, err
		}
		menu := text.NewFile(tmpDisk)
		menu.Unread = false
		line := f.Name.UTF8() + "\n"
		if _, _, err := menu.LoadInitial(strings.NewReader(line)); err != nil {
			_ = menu.Close()
			_ = tmpDisk.Close()
			return nil, err
		}
		got, ok, err := pat.Execute(menu, 0, text.Posn(menu.B.Len()))
		_ = menu.Close()
		_ = tmpDisk.Close()
		if err != nil {
			return nil, err
		}
		if ok && got.P[0].P1 >= 0 {
			if match != nil {
				return nil, fmt.Errorf("multiple files match")
			}
			match = f
		}
	}
	if match == nil {
		return nil, fmt.Errorf("no file matches")
	}
	return match, nil
}

func maxPosn() text.Posn {
	return text.Posn(math.MaxInt64 / 2)
}
