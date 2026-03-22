package workspace

// Workspace will own the authoritative shared editing state.
//
// The real implementation will wrap the sam-compatible core engine and expose
// client-facing operations through the protocol layer.
type Workspace struct{}

// New constructs an empty workspace.
func New() *Workspace {
	return &Workspace{}
}
