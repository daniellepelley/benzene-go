package healthcheck

import (
	"context"
	"errors"
	"testing"
)

// fakeUsage builds a diskCheck-usage function returning fixed values, so the threshold bands can be
// exercised deterministically without touching the real filesystem.
func fakeUsage(free, total uint64, err error) func(string) (uint64, uint64, error) {
	return func(string) (uint64, uint64, error) { return free, total, err }
}

// On a unix host (this CI), the real check against "/" succeeds and reports non-zero totals.
func TestDiskSpaceCheck_RealRootIsOk(t *testing.T) {
	res := DiskSpaceCheck("disk", "/").Check(context.Background())
	if res.Type != "Disk" {
		t.Errorf("Type = %q, want Disk", res.Type)
	}
	if res.Status != StatusOk {
		t.Fatalf("status = %q, want ok (no threshold set)", res.Status)
	}
	total, ok := res.Data["totalBytes"].(uint64)
	if !ok || total == 0 {
		t.Errorf("totalBytes = %v, want a non-zero uint64 for a real filesystem", res.Data["totalBytes"])
	}
	if _, ok := res.Data["freeBytes"].(uint64); !ok {
		t.Errorf("freeBytes missing/!uint64: %v", res.Data["freeBytes"])
	}
	if _, ok := res.Data["usedPercent"]; !ok {
		t.Error("usedPercent missing for a real filesystem")
	}
}

func TestDiskSpaceCheck_Thresholds(t *testing.T) {
	const gib = 1 << 30
	tests := []struct {
		name         string
		free, total  uint64
		min, warn    uint64
		wantStatus   Status
		wantUsedPct  float64
		wantNoUsePct bool
	}{
		{name: "no threshold is ok", free: 5 * gib, total: 10 * gib, wantStatus: StatusOk, wantUsedPct: 50},
		{name: "above both thresholds", free: 5 * gib, total: 10 * gib, min: gib, warn: 2 * gib, wantStatus: StatusOk, wantUsedPct: 50},
		{name: "in warning band", free: 3 * gib / 2, total: 10 * gib, min: gib, warn: 2 * gib, wantStatus: StatusWarning, wantUsedPct: 85},
		{name: "below minimum fails", free: gib / 2, total: 10 * gib, min: gib, warn: 2 * gib, wantStatus: StatusFailed, wantUsedPct: 95},
		{name: "warning set but min not, low free warns", free: gib / 2, total: 10 * gib, warn: gib, wantStatus: StatusWarning, wantUsedPct: 95},
		{name: "zero total omits usedPercent", free: 0, total: 0, wantStatus: StatusOk, wantNoUsePct: true},
		{name: "free above total clamps usedPercent to 0", free: 11 * gib, total: 10 * gib, wantStatus: StatusOk, wantUsedPct: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := diskCheck{name: "disk", path: "/data", usage: fakeUsage(tt.free, tt.total, nil),
				minFreeBytes: tt.min, warnFreeBytes: tt.warn}
			res := c.Check(context.Background())
			if res.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", res.Status, tt.wantStatus)
			}
			if res.Data["path"] != "/data" {
				t.Errorf("path = %v, want /data", res.Data["path"])
			}
			if tt.wantNoUsePct {
				if _, ok := res.Data["usedPercent"]; ok {
					t.Errorf("usedPercent present, want omitted for zero total")
				}
			} else if got := res.Data["usedPercent"].(float64); got != tt.wantUsedPct {
				t.Errorf("usedPercent = %v, want %v", got, tt.wantUsedPct)
			}
		})
	}
}

func TestDiskSpaceCheck_StatErrorIsFailed(t *testing.T) {
	c := diskCheck{name: "disk", path: "/nope", usage: fakeUsage(0, 0, errors.New("no such file"))}
	res := c.Check(context.Background())
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Data["error"] != "stat-error" {
		t.Errorf("error = %v, want the coarse stat-error category (not the raw message)", res.Data["error"])
	}
	if _, ok := res.Data["freeBytes"]; ok {
		t.Error("freeBytes should be absent on a stat error")
	}
}

func TestDiskSpaceCheck_UnsupportedPlatformCategory(t *testing.T) {
	c := diskCheck{name: "disk", path: "/", usage: fakeUsage(0, 0, errUnsupportedPlatform)}
	res := c.Check(context.Background())
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Data["error"] != "unsupported-platform" {
		t.Errorf("error = %v, want unsupported-platform", res.Data["error"])
	}
}

func TestDiskSpaceCheck_Name(t *testing.T) {
	if got := DiskSpaceCheck("free-space", "/").Name(); got != "free-space" {
		t.Errorf("Name = %q, want free-space", got)
	}
}

// Exercises the option funcs and the option loop through the public constructor: a real "/" has far
// more than a couple of bytes free, so tiny thresholds leave it ok.
func TestDiskSpaceCheck_OptionsApplied(t *testing.T) {
	c, ok := DiskSpaceCheck("disk", "/", WithMinimumFreeBytes(1), WithWarningFreeBytes(2)).(diskCheck)
	if !ok {
		t.Fatal("DiskSpaceCheck did not return a diskCheck")
	}
	if c.minFreeBytes != 1 || c.warnFreeBytes != 2 {
		t.Errorf("thresholds = (%d,%d), want (1,2)", c.minFreeBytes, c.warnFreeBytes)
	}
	if res := c.Check(context.Background()); res.Status != StatusOk {
		t.Errorf("status = %q, want ok (root has more than 2 bytes free)", res.Status)
	}
}
