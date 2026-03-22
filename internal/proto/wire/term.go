package wire

// TermService is the initial server-side surface needed by the terminal client.
type TermService interface {
	DownloadService
	CurrentText() (string, error)
}
