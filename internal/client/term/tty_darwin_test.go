//go:build darwin

package term

import (
	"syscall"
	"testing"
)

func TestConfigureCBreakTermiosDisablesInterruptLiteralNextAndStartStopChars(t *testing.T) {
	t.Parallel()

	termios := syscall.Termios{
		Iflag: syscall.ICRNL | syscall.IXON | syscall.IXOFF,
		Lflag: syscall.ICANON | syscall.ECHO,
	}
	termios.Cc[syscall.VINTR] = 0x03
	termios.Cc[syscall.VLNEXT] = 0x16
	termios.Cc[syscall.VSTART] = 0x11
	termios.Cc[syscall.VSTOP] = 0x13

	configureCBreakTermios(&termios)

	if termios.Iflag&syscall.ICRNL != 0 {
		t.Fatalf("Iflag = %#x, want ICRNL cleared", termios.Iflag)
	}
	if termios.Iflag&(syscall.IXON|syscall.IXOFF) != (syscall.IXON | syscall.IXOFF) {
		t.Fatalf("Iflag = %#x, want IXON/IXOFF preserved", termios.Iflag)
	}
	if termios.Lflag&(syscall.ICANON|syscall.ECHO) != 0 {
		t.Fatalf("Lflag = %#x, want ICANON/ECHO cleared", termios.Lflag)
	}
	if got, want := termios.Cc[syscall.VINTR], uint8(posixVDisable); got != want {
		t.Fatalf("VINTR = %#x, want %#x", got, want)
	}
	if got, want := termios.Cc[syscall.VLNEXT], uint8(posixVDisable); got != want {
		t.Fatalf("VLNEXT = %#x, want %#x", got, want)
	}
	if got, want := termios.Cc[syscall.VSTART], uint8(posixVDisable); got != want {
		t.Fatalf("VSTART = %#x, want %#x", got, want)
	}
	if got, want := termios.Cc[syscall.VSTOP], uint8(posixVDisable); got != want {
		t.Fatalf("VSTOP = %#x, want %#x", got, want)
	}
	if got, want := termios.Cc[syscall.VMIN], uint8(1); got != want {
		t.Fatalf("VMIN = %d, want %d", got, want)
	}
	if got, want := termios.Cc[syscall.VTIME], uint8(0); got != want {
		t.Fatalf("VTIME = %d, want %d", got, want)
	}
}
