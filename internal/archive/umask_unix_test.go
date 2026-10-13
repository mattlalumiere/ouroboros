//go:build !windows

package archive

import "syscall"

func umaskSet(m int) int { return syscall.Umask(m) }
