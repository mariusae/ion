package term

import "syscall"

func timevalFromUsec(timeoutUsec int64) syscall.Timeval {
	return syscall.NsecToTimeval(timeoutUsec * 1_000)
}
