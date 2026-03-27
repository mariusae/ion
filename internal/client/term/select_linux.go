//go:build linux

package term

import "syscall"

func selectRead(nfd int, readfds *syscall.FdSet, timeout *syscall.Timeval) error {
	_, err := syscall.Select(nfd, readfds, nil, nil, timeout)
	return err
}
