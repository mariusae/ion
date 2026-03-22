//go:build darwin

package term

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

type ttyState struct {
	fd   int
	orig syscall.Termios
}

func enterCBreakMode(f *os.File) (*ttyState, error) {
	fd := int(f.Fd())
	var termios syscall.Termios
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCGETA),
		uintptr(unsafe.Pointer(&termios)),
		0, 0, 0,
	); errno != 0 {
		return nil, fmt.Errorf("get terminal mode: %w", errno)
	}
	orig := termios
	termios.Lflag &^= syscall.ICANON
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCSETA),
		uintptr(unsafe.Pointer(&termios)),
		0, 0, 0,
	); errno != 0 {
		return nil, fmt.Errorf("set terminal mode: %w", errno)
	}
	return &ttyState{fd: fd, orig: orig}, nil
}

func (s *ttyState) restore() error {
	if s == nil {
		return nil
	}
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(s.fd),
		uintptr(syscall.TIOCSETA),
		uintptr(unsafe.Pointer(&s.orig)),
		0, 0, 0,
	); errno != 0 {
		return fmt.Errorf("restore terminal mode: %w", errno)
	}
	return nil
}
