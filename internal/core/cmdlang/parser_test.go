package cmdlang

import "testing"

func TestParsePrintWithRange(t *testing.T) {
	t.Parallel()

	p := NewParser("1,2p\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != 'p' {
		t.Fatalf("Cmdc = %q, want 'p'", cmd.Cmdc)
	}
	if cmd.Addr == nil || cmd.Addr.Type != ',' {
		t.Fatalf("Addr = %#v, want compound ','", cmd.Addr)
	}
	if cmd.Addr.Left == nil || cmd.Addr.Left.Type != 'l' || cmd.Addr.Left.Num != 1 {
		t.Fatalf("left addr = %#v", cmd.Addr.Left)
	}
	if cmd.Addr.Next == nil || cmd.Addr.Next.Type != 'l' || cmd.Addr.Next.Num != 2 {
		t.Fatalf("right addr = %#v", cmd.Addr.Next)
	}
}

func TestParseSubstitute(t *testing.T) {
	t.Parallel()

	p := NewParser("s/x/y/g\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != 's' {
		t.Fatalf("Cmdc = %q, want 's'", cmd.Cmdc)
	}
	if cmd.Re == nil || cmd.Re.UTF8() != "x\x00" {
		t.Fatalf("Re = %q, want %q", cmd.Re.UTF8(), "x\x00")
	}
	if cmd.Text == nil || cmd.Text.UTF8() != "y" {
		t.Fatalf("Text = %q, want %q", cmd.Text.UTF8(), "y")
	}
	if cmd.Flag != 'g' {
		t.Fatalf("Flag = %q, want 'g'", cmd.Flag)
	}
}

func TestParseAppendText(t *testing.T) {
	t.Parallel()

	p := NewParser("a\nhello\n.\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != 'a' {
		t.Fatalf("Cmdc = %q, want 'a'", cmd.Cmdc)
	}
	if cmd.Text == nil || cmd.Text.UTF8() != "hello\n\x00" {
		t.Fatalf("Text = %q, want %q", cmd.Text.UTF8(), "hello\n\x00")
	}
}

func TestParseRegexpDefaultCommand(t *testing.T) {
	t.Parallel()

	p := NewParser(",x/a/\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != 'x' {
		t.Fatalf("Cmdc = %q, want 'x'", cmd.Cmdc)
	}
	if cmd.Cmd == nil || cmd.Cmd.Cmdc != 'p' {
		t.Fatalf("default nested command = %#v, want 'p'", cmd.Cmd)
	}
}

func TestParseBlock(t *testing.T) {
	t.Parallel()

	p := NewParser("{\np\nq\n}\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != '{' {
		t.Fatalf("Cmdc = %q, want '{'", cmd.Cmdc)
	}
	if cmd.Cmd == nil || cmd.Cmd.Cmdc != 'p' || cmd.Cmd.Next == nil || cmd.Cmd.Next.Cmdc != 'q' {
		t.Fatalf("block commands = %#v", cmd.Cmd)
	}
}

func TestParseNeedsMoreInputForTextCommand(t *testing.T) {
	t.Parallel()

	p := NewParserRunes([]rune("a\nhello\n"))
	if _, err := p.ParseWithFinal(false); err != ErrNeedMoreInput {
		t.Fatalf("ParseWithFinal(false) error = %v, want %v", err, ErrNeedMoreInput)
	}
}

func TestParsePreservesLastPatternAcrossReset(t *testing.T) {
	t.Parallel()

	p := NewParserRunes([]rune("s/x/y/\n"))
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("first Parse() error = %v", err)
	}
	if cmd.Re == nil || cmd.Re.UTF8() != "x\x00" {
		t.Fatalf("first Re = %q, want %q", cmd.Re.UTF8(), "x\x00")
	}

	p.ResetRunes([]rune("s//z/\n"))
	cmd, err = p.Parse()
	if err != nil {
		t.Fatalf("second Parse() error = %v", err)
	}
	if cmd.Re == nil || cmd.Re.UTF8() != "x\x00" {
		t.Fatalf("second Re = %q, want %q", cmd.Re.UTF8(), "x\x00")
	}
	if cmd.Text == nil || cmd.Text.UTF8() != "z" {
		t.Fatalf("second Text = %q, want %q", cmd.Text.UTF8(), "z")
	}
}

func TestParseNamespacedCommand(t *testing.T) {
	t.Parallel()

	p := NewParser(":help :ns\n")
	cmd, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cmd.Cmdc != ':' {
		t.Fatalf("Cmdc = %q, want ':'", cmd.Cmdc)
	}
	if cmd.Text == nil || cmd.Text.UTF8() != "help :ns\x00" {
		t.Fatalf("Text = %q, want %q", cmd.Text.UTF8(), "help :ns\x00")
	}
}

func TestParseUnknownCommandErrorMatchesSam(t *testing.T) {
	t.Parallel()

	p := NewParser("Z\n")
	if _, err := p.Parse(); err == nil || err.Error() != "unknown command `Z'" {
		t.Fatalf("Parse() error = %v, want %q", err, "unknown command `Z'")
	}
}

func TestParseBadDelimiterErrorMatchesSam(t *testing.T) {
	t.Parallel()

	p := NewParser("sptp\n")
	if _, err := p.Parse(); err == nil || err.Error() != "bad delimiter `p'" {
		t.Fatalf("Parse() error = %v, want %q", err, "bad delimiter `p'")
	}
}

func TestParseUnmatchedBlockErrorMatchesSam(t *testing.T) {
	t.Parallel()

	p := NewParser("}\n")
	if _, err := p.Parse(); err == nil || err.Error() != "unmatched `}'" {
		t.Fatalf("Parse() error = %v, want %q", err, "unmatched `}'")
	}
}
