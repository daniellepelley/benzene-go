//go:build !unix && !windows

package healthcheck

// diskUsage has no implementation on platforms that are neither unix nor windows (e.g. js/wasm,
// plan9): there is no portable free-space syscall to bind. It reports errUnsupportedPlatform, which
// DiskSpaceCheck surfaces as a "unsupported-platform" failed result rather than failing to compile -
// so a service can still include the check in a cross-platform build and simply see it report
// unsupported there.
func diskUsage(_ string) (free, total uint64, err error) {
	return 0, 0, errUnsupportedPlatform
}
