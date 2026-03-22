package term

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"ion/internal/client/download"
	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
)

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stdout, stderr io.Writer, svc wire.TermService) error {
	inFile, ok := stdin.(*os.File)
	if !ok || !isTTY(inFile) {
		return download.Run(files, stdin, stderr, svc)
	}
	if err := svc.Bootstrap(files); err != nil {
		return err
	}
	return runTTY(inFile, stdout, stderr, svc)
}

func runTTY(stdin *os.File, stdout, stderr io.Writer, svc wire.TermService) error {
	state, err := enterCBreakMode(stdin)
	if err != nil {
		return err
	}
	defer state.restore()

	parser := cmdlang.NewParserRunes(nil)
	reader := bufio.NewReader(stdin)
	var pending []rune
	var linebuf []rune
	inBufferMode := false

	executePending := func(final bool) (bool, error) {
		for {
			parser.ResetRunes(pending)
			cmd, err := parser.ParseWithFinal(final)
			if err != nil {
				if errors.Is(err, cmdlang.ErrNeedMoreInput) {
					return false, nil
				}
				if _, werr := fmt.Fprintf(stderr, "?%v\n", err); werr != nil {
					return false, werr
				}
				if !final {
					pending = discardFailedCommand(pending)
				} else {
					pending = nil
				}
				return false, nil
			}

			consumed := parser.Consumed()
			if consumed > 0 {
				pending = pending[consumed:]
			}
			if cmd == nil {
				return false, nil
			}

			ok, err := svc.Execute(cmd)
			if err != nil {
				if _, werr := fmt.Fprintf(stderr, "?%v\n", err); werr != nil {
					return false, werr
				}
				continue
			}
			if !ok {
				return true, nil
			}
		}
	}

	submitLine := func() (bool, error) {
		if _, err := io.WriteString(stdout, "\n"); err != nil {
			return false, err
		}
		pending = append(pending, linebuf...)
		pending = append(pending, '\n')
		linebuf = linebuf[:0]
		return executePending(false)
	}

	eraseLast := func() error {
		if len(linebuf) == 0 {
			return nil
		}
		linebuf = linebuf[:len(linebuf)-1]
		_, err := io.WriteString(stdout, "\b \b")
		return err
	}

	ctrlC := func() (bool, error) {
		if _, err := io.WriteString(stdout, "^C\n"); err != nil {
			return false, err
		}
		linebuf = linebuf[:0]
		pending = append(pending, '\n')
		return executePending(false)
	}

	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				pending = append(pending, linebuf...)
				_, err := executePending(true)
				if err != nil {
					return err
				}
				return nil
			}
			return err
		}
		if r == '\r' {
			r = '\n'
		}

		if r == 0x1b {
			if inBufferMode {
				if err := exitBufferMode(stdout); err != nil {
					return err
				}
				inBufferMode = false
			} else {
				if err := enterBufferMode(stdout, svc); err != nil {
					return err
				}
				inBufferMode = true
			}
			continue
		}
		if inBufferMode {
			continue
		}

		switch r {
		case '\n':
			done, err := submitLine()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case 0x7f, 0x08:
			if err := eraseLast(); err != nil {
				return err
			}
		case 0x15:
			for len(linebuf) > 0 {
				if err := eraseLast(); err != nil {
					return err
				}
			}
		case 0x17:
			for len(linebuf) > 0 && linebuf[len(linebuf)-1] == ' ' {
				if err := eraseLast(); err != nil {
					return err
				}
			}
			for len(linebuf) > 0 && linebuf[len(linebuf)-1] != ' ' {
				if err := eraseLast(); err != nil {
					return err
				}
			}
		case 0x03:
			done, err := ctrlC()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case 0x04:
			if len(linebuf) != 0 {
				break
			}
			pending = append(pending, linebuf...)
			done, err := executePending(true)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			return nil
		default:
			if r >= 32 || r == '\t' {
				linebuf = append(linebuf, r)
				if _, err := io.WriteString(stdout, string(r)); err != nil {
					return err
				}
			}
		}
	}
}

func enterBufferMode(stdout io.Writer, svc wire.TermService) error {
	text, err := svc.CurrentText()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[2J\x1b[H"); err != nil {
		return err
	}
	_, err = io.WriteString(stdout, text)
	return err
}

func exitBufferMode(stdout io.Writer) error {
	_, err := io.WriteString(stdout, "\x1b[?1049l")
	return err
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func discardFailedCommand(pending []rune) []rune {
	for i, r := range pending {
		if r == '\n' {
			return pending[i+1:]
		}
	}
	return nil
}
