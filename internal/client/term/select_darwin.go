//go:build darwin

package term

import "syscall"

func selectRead(nfd int, readfds *syscall.FdSet, timeout *syscall.Timeval) error {
	return syscall.Select(nfd, readfds, nil, nil, timeout)
}
