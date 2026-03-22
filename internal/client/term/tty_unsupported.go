//go:build !darwin

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

func (s *ttyState) restore() error {
	_ = s
	return nil
}
