//go:build windows

package archive

func umaskSet(m int) int { return 0 }
