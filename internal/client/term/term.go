package term

import (
	"io"

	"ion/internal/client/download"
	"ion/internal/proto/wire"
)

// Run executes the initial terminal-client slice.
//
// For now this shares the command-mode loop with the download client while the
// full terminal UI from term.c is ported behind this package boundary.
func Run(files []string, stdin io.Reader, stderr io.Writer, svc wire.DownloadService) error {
	return download.Run(files, stdin, stderr, svc)
}
