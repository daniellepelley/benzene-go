package healthcheck

import (
	"context"
	"errors"
	"math"
)

// This file holds the DiskSpaceCheck host self-check - the Go equivalent of Benzene.HealthChecks.Disk.
// It reports the free space on the filesystem backing a path and, when a threshold is configured, flips
// to warning/failed as that space runs low. Free-space reporting has no portable standard-library API,
// so the one platform-specific call (diskUsage) lives behind build tags: disk_unix.go (syscall.Statfs),
// disk_windows.go (GetDiskFreeSpaceExW via a lazy kernel32 binding), and disk_other.go (an unsupported
// fallback). All three are zero-dependency - the split is a build-tag concern, not a new dependency.

// errUnsupportedPlatform is returned by diskUsage on a platform with no free-space implementation
// (disk_other.go). It is reported as the coarse "unsupported-platform" category, never a raw message.
var errUnsupportedPlatform = errors.New("disk usage not supported on this platform")

// DiskSpaceOption configures a DiskSpaceCheck.
type DiskSpaceOption func(*diskCheck)

// WithMinimumFreeBytes sets the failure threshold: when the filesystem's free space is below n bytes
// the check reports StatusFailed. Default 0 - no floor, so without this (and WithWarningFreeBytes) the
// check only reports usage and never fails on space. Set it to gate readiness on free space.
func WithMinimumFreeBytes(n uint64) DiskSpaceOption {
	return func(c *diskCheck) { c.minFreeBytes = n }
}

// WithWarningFreeBytes sets the warning threshold: when free space is below n bytes but at or above the
// minimum, the check reports StatusWarning (which surfaces in the report but does not, on its own, flip
// the service to unhealthy). Default 0 - no warning band. Set it below the failure floor for an
// early-warning signal.
func WithWarningFreeBytes(n uint64) DiskSpaceOption {
	return func(c *diskCheck) { c.warnFreeBytes = n }
}

// DiskSpaceCheck reports the free space on the filesystem containing path and, if a threshold is set,
// gates health on it: below WithMinimumFreeBytes -> StatusFailed, below WithWarningFreeBytes (but at or
// above the minimum) -> StatusWarning, otherwise StatusOk. With no threshold it is pure telemetry
// (always ok, with the usage in Data). Matches Benzene.HealthChecks.Disk. name identifies the check;
// the reported Type is "Disk". Data always carries the path and, on success, freeBytes / totalBytes /
// usedPercent; on a stat error it carries a coarse category ("stat-error" / "unsupported-platform"),
// never the raw OS message.
//
// "Free" is the space available to an ordinary (unprivileged) process - the app-relevant figure - so
// it can read slightly below the raw free blocks a filesystem reserves for root.
func DiskSpaceCheck(name, path string, opts ...DiskSpaceOption) Check {
	c := diskCheck{name: name, path: path, usage: diskUsage}
	for _, o := range opts {
		o(&c)
	}
	return c
}

type diskCheck struct {
	name          string
	path          string
	minFreeBytes  uint64
	warnFreeBytes uint64
	// usage resolves free/total bytes for the volume backing path. It defaults to the platform
	// diskUsage and is a field (rather than a direct call) so tests can drive the threshold logic with
	// deterministic values without touching the real filesystem.
	usage func(path string) (free, total uint64, err error)
}

func (c diskCheck) Name() string { return c.name }

func (c diskCheck) Check(_ context.Context) CheckResult {
	data := map[string]any{"path": c.path}
	free, total, err := c.usage(c.path)
	if err != nil {
		data["error"] = diskErrorCategory(err)
		return CheckResult{Status: StatusFailed, Type: "Disk", Data: data}
	}
	data["freeBytes"] = free
	data["totalBytes"] = total
	if total > 0 {
		used := float64(total) - float64(free)
		if used < 0 { // defensive: available never exceeds total on a real filesystem
			used = 0
		}
		data["usedPercent"] = math.Round(used/float64(total)*10000) / 100
	}

	switch {
	case c.minFreeBytes > 0 && free < c.minFreeBytes:
		return CheckResult{Status: StatusFailed, Type: "Disk", Data: data}
	case c.warnFreeBytes > 0 && free < c.warnFreeBytes:
		return CheckResult{Status: StatusWarning, Type: "Disk", Data: data}
	default:
		return CheckResult{Status: StatusOk, Type: "Disk", Data: data}
	}
}

// diskErrorCategory maps a diskUsage error to a coarse, non-sensitive category, matching the TCP/HTTP
// checks' choice to report a category rather than the raw OS error text.
func diskErrorCategory(err error) string {
	if errors.Is(err, errUnsupportedPlatform) {
		return "unsupported-platform"
	}
	return "stat-error"
}
