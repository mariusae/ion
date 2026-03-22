package wire

import "ion/internal/core/cmdlang"

// DownloadService is the minimal server-side surface needed by the sam -d
// compatible client loop.
type DownloadService interface {
	Bootstrap(files []string) error
	Execute(cmd *cmdlang.Cmd) (bool, error)
}
