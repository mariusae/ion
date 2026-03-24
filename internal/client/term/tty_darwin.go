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

type winsize struct {
	row    uint16
	col    uint16
	xpixel uint16
	ypixel uint16
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
	configureCBreakTermios(&termios)
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

func configureCBreakTermios(termios *syscall.Termios) {
	if termios == nil {
		return
	}
	// Disable canonical input, echo, CR translation, and software flow control
	// so editor control keys like Ctrl-Q are delivered to the client.
	termios.Iflag &^= syscall.ICRNL | syscall.IXON | syscall.IXOFF
	termios.Lflag &^= syscall.ICANON | syscall.ECHO
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
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

func terminalSize(f *os.File) (rows, cols int, err error) {
	var ws winsize
	if _, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
		0, 0, 0,
	); errno != 0 {
		return 0, 0, fmt.Errorf("get terminal size: %w", errno)
	}
	return int(ws.row), int(ws.col), nil
}
