// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Sievehouse/gpu-lens/internal/config"
)

func fakeAMDPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the amd-smi test double is a POSIX shell script")
	}
	p, err := filepath.Abs("../../tests/fixtures/fake-amd-smi")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAMDSMIAllowList(t *testing.T) {
	for _, ok := range [][]string{{"metric", "--json"}, {"static", "--json"}, {"list", "--json"}, {"process", "--json"}} {
		if err := validateAMDSMIArgs(ok); err != nil {
			t.Errorf("%v should be allowed: %v", ok, err)
		}
	}
	for _, bad := range [][]string{{"reset", "--gpureset", "-g", "0"}, {"set", "--power-cap", "100"}, {"metric"}, {"metric", "--json", "--watch"}, {"metric", "--csv"}} {
		if err := validateAMDSMIArgs(bad); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
}

func TestAMDSMIParsesAllLayouts(t *testing.T) {
	path := fakeAMDPath(t)
	m := config.DefaultMetricMapping()
	for _, format := range []string{"modern", "legacy", "wrapped"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("FAKE_AMD_FORMAT", format)
			t.Setenv("FAKE_AMD_TEMP0", "71")
			res, err := NewAMDSMISource(path, 0, nil).Collect(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.GPUs) != 2 {
				t.Fatalf("want 2 GPUs, got %d (%v)", len(res.GPUs), res.Notes)
			}
			g0, g1 := res.GPUs[0], res.GPUs[1]
			if g0.UUID != "1fff74a1-0000-1000-80ff-2f9e47e3eb6c" || g0.ModelName != "AMD Instinct MI300X" || g0.PCIBusID != "0000:0c:00.0" {
				t.Fatalf("identity: %+v", g0)
			}
			if g0.Latest.GPUUtilPercent != 88 || g0.Latest.GPUTempCelsius != 71 || g0.Latest.PowerUsageWatts != 612 || g0.PowerLimitWatts != 750 {
				t.Fatalf("metrics: %+v limit=%v", g0.Latest, g0.PowerLimitWatts)
			}
			if g0.TotalMemoryMiB != 196592 || g0.Latest.FBUsedMiB != 151000 || g0.Latest.MemoryTempCelsius != 58 {
				t.Fatalf("memory: total=%v used=%v memtemp=%v", g0.TotalMemoryMiB, g0.Latest.FBUsedMiB, g0.Latest.MemoryTempCelsius)
			}
			if g1.Latest.GPUTempCelsius != 39 {
				t.Fatalf("GPU 1 should fall back to the hotspot sensor, got %v", g1.Latest.GPUTempCelsius)
			}
			if g1.Latest.ECCSBEVolTotal == nil || *g1.Latest.ECCSBEVolTotal != 3 || res.DriverVersion != "6.7.0" {
				t.Fatalf("ecc/driver: %+v %q", g1.Latest.ECCSBEVolTotal, res.DriverVersion)
			}
			ex := res.Extras[g0.UUID]
			if ex.FanSpeedPercent != nil {
				t.Fatal("a fan reported as N/A must stay unknown, not 0")
			}
			if ex.PCIe == nil || ex.PCIe.GenCurrent != 5 || ex.PCIe.WidthMax != 16 {
				t.Fatalf("pcie: %+v", ex.PCIe)
			}
			if len(res.Processes[g0.UUID]) != 1 || res.Processes[g0.UUID][0].PID != 4242 || *res.Processes[g0.UUID][0].UsedMemoryMiB < 150000 {
				t.Fatalf("processes: %+v", res.Processes)
			}
			for _, metric := range []string{m.GPUUtilMetric, m.GPUTempMetric, m.PowerUsageMetric, m.FBUsedMetric} {
				if !res.Exported[metric] {
					t.Errorf("%s should be marked exported", metric)
				}
			}
			if res.Exported[m.XIDErrorsMetric] {
				t.Error("AMD has no Xid metric; it must not be marked exported")
			}
		})
	}
}

func TestAMDSMIFailureModes(t *testing.T) {
	path := fakeAMDPath(t)
	m := config.DefaultMetricMapping()
	t.Setenv("FAKE_AMD_MODE", "no-devices")
	res, err := NewAMDSMISource(path, 0, nil).Collect(context.Background(), m)
	if err != nil || !res.NoDevices {
		t.Fatalf("no-devices: %+v %v", res, err)
	}
	t.Setenv("FAKE_AMD_MODE", "driver-fail")
	if _, err := NewAMDSMISource(path, 0, nil).Collect(context.Background(), m); err == nil {
		t.Fatal("driver failure must be an error")
	}
	if _, err := NewAMDSMISource("/nonexistent/amd-smi", 0, nil).Collect(context.Background(), m); !errors.Is(err, ErrAMDSMINotFound) {
		t.Fatalf("missing binary: %v", err)
	}
}
