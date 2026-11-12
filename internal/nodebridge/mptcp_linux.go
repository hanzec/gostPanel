package nodebridge

import "golang.org/x/sys/unix"

func detectMultipathTCP() bool {
	// Go's ListenConfig silently falls back to TCP, so probe the kernel protocol directly.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_MPTCP)
	if err != nil {
		return false
	}
	_ = unix.Close(fd)
	return true
}
