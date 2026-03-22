package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"ion/internal/core/cmdlang"
	"ion/internal/core/exec"
	"ion/internal/core/text"
)

type config struct {
	download bool
	files    []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ion: %v\n", err)
		return 2
	}

	if cfg.download {
		if err := runDownload(cfg, stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ion: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(stderr, "ion: terminal mode is not implemented yet")
	return 1
}

func parseArgs(args []string) (config, error) {
	var cfg config

	fs := flag.NewFlagSet("ion", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&cfg.download, "d", false, "run in command-line download mode")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	cfg.files = fs.Args()
	return cfg, nil
}

func runDownload(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	sess := exec.NewSession(stdout)
	sess.Diag = stderr

	if len(cfg.files) == 0 {
		d, err := text.NewDisk()
		if err != nil {
			return err
		}
		f := text.NewFile(d)
		f.Unread = false
		sess.AddFile(f)
	} else {
		for _, name := range cfg.files {
			d, err := text.NewDisk()
			if err != nil {
				return err
			}
			f := text.NewFile(d)
			s := text.NewStringFromUTF8(name)
			if err := f.Name.DupString(&s); err != nil {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if _, _, err := f.LoadInitial(bytes.NewReader(data)); err != nil {
				return err
			}
			sess.AddFile(f)
		}
	}

	if sess.Current != nil {
		if err := printFileStatus(stderr, sess.Current, true); err != nil {
			return err
		}
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
				return false, err
			}

			consumed := parser.Consumed()
			if consumed > 0 {
				pending = pending[consumed:]
			}

			if cmd == nil {
				return false, nil
			}

			ok, err := sess.Execute(cmd)
			if err != nil {
				return false, err
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

func printFileStatus(w io.Writer, f *text.File, current bool) error {
	mod := ' '
	if f.Mod {
		mod = '\''
	}
	rasp := '-'
	cur := ' '
	if current {
		cur = '.'
	}
	_, err := fmt.Fprintf(w, "%c%c%c %s\n", mod, rasp, cur, strings.TrimRight(f.Name.UTF8(), "\x00"))
	return err
}
