//go:build !windows

package main

import "syscall"

// diskSpace: 路径所在磁盘的可用 / 总空间(字节)。
func diskSpace(path string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, true
}
