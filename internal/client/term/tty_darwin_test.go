//go:build darwin

package term

import (
	"syscall"
	"testing"
)

func TestConfigureCBreakTermiosDisablesFlowControl(t *testing.T) {
	t.Parallel()

	termios := syscall.Termios{
		Iflag: syscall.ICRNL | syscall.IXON | syscall.IXOFF,
		Lflag: syscall.ICANON | syscall.ECHO,
	}

	configureCBreakTermios(&termios)

	if termios.Iflag&(syscall.ICRNL|syscall.IXON|syscall.IXOFF) != 0 {
		t.Fatalf("Iflag = %#x, want ICRNL/IXON/IXOFF cleared", termios.Iflag)
	}
	if termios.Lflag&(syscall.ICANON|syscall.ECHO) != 0 {
		t.Fatalf("Lflag = %#x, want ICANON/ECHO cleared", termios.Lflag)
	}
	if got, want := termios.Cc[syscall.VMIN], uint8(1); got != want {
		t.Fatalf("VMIN = %d, want %d", got, want)
	}
	if got, want := termios.Cc[syscall.VTIME], uint8(0); got != want {
		t.Fatalf("VTIME = %d, want %d", got, want)
	}
}
