//go:build !darwin && !linux

package term

import (
	"fmt"
	"os"
)

type ttyState struct{}

func enterCBreakMode(f *os.File) (*ttyState, error) {
	_ = f
	return nil, fmt.Errorf("tty mode not supported on this platform")
}

func terminalSize(f *os.File) (rows, cols int, err error) {
	_ = f
	return 0, 0, fmt.Errorf("terminal size not supported on this platform")
}

func (s *ttyState) restore() error {
	_ = s
	return nil
}
