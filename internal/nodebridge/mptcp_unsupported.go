//go:build !linux

package nodebridge

func detectMultipathTCP() bool { return false }
