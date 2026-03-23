package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"ion/internal/client/download"
	"ion/internal/client/term"
	serversession "ion/internal/server/session"
	"ion/internal/server/workspace"
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

	if err := runTerm(cfg, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "ion: %v\n", err)
		return 1
	}
	return 0
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
	ws := workspace.New()
	return download.Run(cfg.files, stdin, stderr, serversession.NewDownload(ws, stdout, stderr))
}

func runTerm(cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	capture := term.NewOutputCapture(stdout, stderr)
	ws := workspace.New()
	return term.Run(cfg.files, stdin, stdout, stderr, serversession.NewTerm(ws, capture.Stdout(), capture.Stderr()), capture)
}
