//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// diskSpace: 路径所在磁盘的可用 / 总空间(字节)。
func diskSpace(path string) (free, total uint64, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, 0, false
	}
	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return 0, 0, false
	}
	var avail, tot, totFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&tot)), uintptr(unsafe.Pointer(&totFree)))
	if r == 0 {
		return 0, 0, false
	}
	return avail, tot, true
}
