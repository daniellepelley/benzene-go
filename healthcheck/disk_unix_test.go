//go:build unix

package healthcheck

import "testing"

// The real statfs succeeds for a valid path (covered via "/" in the cross-platform test); this covers
// the error branch - a path that cannot be stat'd returns an error, which DiskSpaceCheck surfaces as a
// failed "stat-error" result.
func TestDiskUsage_Unix_StatError(t *testing.T) {
	if _, _, err := diskUsage("/nonexistent-path-for-benzene-disk-check-\x00"); err == nil {
		t.Error("diskUsage on an unstattable path returned nil error, want an error")
	}
}
