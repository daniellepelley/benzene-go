//go:build windows

package healthcheck

import (
	"syscall"
	"unsafe"
)

// The free-space call is GetDiskFreeSpaceExW from kernel32, bound lazily so this stays
// zero-dependency (no golang.org/x/sys). NewLazyDLL/NewProc resolve on first use.
var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// diskUsage reports free (available-to-caller) and total bytes for the volume containing path, via
// GetDiskFreeSpaceExW. lpFreeBytesAvailableToCaller is the Windows analogue of unix Bavail - the space
// the calling user may actually use (honoring quotas) - matching the unix implementation's choice.
//
// This path is compiled and cross-compile-verified for windows but not executed in this repo's CI
// (which runs on linux), per the no-fabrication rule: it follows the documented GetDiskFreeSpaceExW
// contract, it is not a guessed shape.
func diskUsage(path string) (free, total uint64, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var freeToCaller, totalBytes, totalFree uint64
	r, _, callErr := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		// A zero return means failure; callErr carries the OS error (never a nil error here).
		return 0, 0, callErr
	}
	return freeToCaller, totalBytes, nil
}
