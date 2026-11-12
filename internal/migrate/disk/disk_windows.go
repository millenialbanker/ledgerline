//go:build windows

package disk

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func free(p string) (int64, error) {
	u, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return 0, err
	}
	var avail, total, totalFree uint64
	r, _, e := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(&avail)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0, e
	}
	return int64(avail), nil
}
