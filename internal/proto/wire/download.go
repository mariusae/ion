package wire

// DownloadService is the minimal server-side surface needed by the sam -d
// compatible client loop.
type DownloadService interface {
	Bootstrap(files []string) error
	Execute(script string) (bool, error)
}
