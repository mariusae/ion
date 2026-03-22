package download

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"ion/internal/core/cmdlang"
	"ion/internal/proto/wire"
)

type diagnosticReporter interface {
	Diagnostic() string
}

// Run drives the sam -d compatible client loop against a server implementation.
func Run(files []string, stdin io.Reader, stderr io.Writer, svc wire.DownloadService) error {
	if err := svc.Bootstrap(files); err != nil {
		return err
	}

	parser := cmdlang.NewParserRunes(nil)
	reader := bufio.NewReader(stdin)
	var pending []rune

	executePending := func(final bool) (bool, error) {
		for {
			parser.ResetRunes(pending)
			cmd, err := parser.ParseWithFinal(final)
			if err != nil {
				if errors.Is(err, cmdlang.ErrNeedMoreInput) {
					return false, nil
				}
				if reportCommandError(stderr, err); !final {
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
				if err := reportCommandError(stderr, err); err != nil {
					return false, err
				}
				continue
			}
			if !ok {
				return true, nil
			}
		}
	}

	for {
		done, err := executePending(false)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if len(line) > 0 {
				pending = append(pending, []rune(line)...)
			}
			done, err := executePending(true)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			return nil
		}

		pending = append(pending, []rune(line)...)
		done, err = executePending(false)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func reportCommandError(w io.Writer, err error) error {
	var diag diagnosticReporter
	if errors.As(err, &diag) {
		_, writeErr := fmt.Fprintln(w, diag.Diagnostic())
		return writeErr
	}
	_, writeErr := fmt.Fprintf(w, "?%v\n", err)
	return writeErr
}

func discardFailedCommand(pending []rune) []rune {
	for i, r := range pending {
		if r == '\n' {
			return pending[i+1:]
		}
	}
	return nil
}
