package session

import (
	"fmt"
	"strings"

	"ion/internal/proto/wire"
)

func (s *DownloadSession) executeDemoCommand(script string) (bool, *wire.BufferView, error) {
	command := strings.TrimSpace(script)
	switch command {
	case ":lsp:describe", ":demo:describe":
		info, err := s.ws.DescribeCurrentSymbol(s.state)
		if err != nil {
			return true, nil, err
		}
		if _, err := fmt.Fprintf(s.stdout, "symbol %s %s:%d:%d [%d,%d)\n", info.Name, info.FileName, info.Line, info.Column, info.Start, info.End); err != nil {
			return true, nil, err
		}
		return true, nil, nil
	case ":lsp:goto", ":demo:goto":
		view, info, err := s.ws.GotoDemoSymbol(s.state)
		if err != nil {
			return true, nil, err
		}
		if _, err := fmt.Fprintf(s.stdout, "goto %s %s:%d:%d\n", info.Name, info.FileName, info.Line, info.Column); err != nil {
			return true, nil, err
		}
		return true, &view, nil
	default:
		return false, nil, nil
	}
}
