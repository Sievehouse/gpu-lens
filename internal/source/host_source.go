// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"os"
	"runtime"
	"sync"
	"time"
)

// PCIGPUDevice is a display-class PCI device found on the local machine.
type PCIGPUDevice struct {
	Address  string `json:"address"`
	Vendor   string `json:"vendor"`
	VendorID string `json:"vendor_id"`
	DeviceID string `json:"device_id"`
	Class    string `json:"class"`
	Driver   string `json:"driver"`
}

// HostPressure holds Linux pressure-stall information (share of the last 10 s some task was stalled).
type HostPressure struct {
	CPUSomeAvg10    *float64 `json:"cpu_some_avg10"`
	MemorySomeAvg10 *float64 `json:"memory_some_avg10"`
	IOSomeAvg10     *float64 `json:"io_some_avg10"`
}

// HostProcessInfo describes the GPU Lens server process itself.
type HostProcessInfo struct {
	PID           int     `json:"pid"`
	UptimeSeconds float64 `json:"uptime_seconds"`
	MemoryMiB     float64 `json:"memory_mib"`
	Workers       int     `json:"workers"`
	Runtime       string  `json:"runtime"`
}

// HostVitals is a point-in-time reading of the machine GPU Lens runs on. Pointer fields are nil when the value
// could not be read on this platform; they are never filled with placeholders.
type HostVitals struct {
	Supported           bool            `json:"supported"`
	CollectedAt         time.Time       `json:"collected_at"`
	Hostname            string          `json:"hostname"`
	OS                  string          `json:"os"`
	Arch                string          `json:"arch"`
	Platform            string          `json:"platform"`
	Kernel              string          `json:"kernel"`
	CPUModel            string          `json:"cpu_model"`
	LogicalCPUs         int             `json:"logical_cpus"`
	InContainer         bool            `json:"in_container"`
	UptimeSeconds       *float64        `json:"uptime_seconds"`
	CPUPercent          *float64        `json:"cpu_percent"`
	CPUIOWaitPercent    *float64        `json:"cpu_iowait_percent"`
	Load1               *float64        `json:"load1"`
	Load5               *float64        `json:"load5"`
	Load15              *float64        `json:"load15"`
	MemTotalMiB         *float64        `json:"mem_total_mib"`
	MemAvailableMiB     *float64        `json:"mem_available_mib"`
	MemUsedPercent      *float64        `json:"mem_used_percent"`
	SwapTotalMiB        *float64        `json:"swap_total_mib"`
	SwapUsedMiB         *float64        `json:"swap_used_mib"`
	DiskPath            string          `json:"disk_path"`
	DiskTotalGiB        *float64        `json:"disk_total_gib"`
	DiskUsedPercent     *float64        `json:"disk_used_percent"`
	NetRxBytesPerSec    *float64        `json:"net_rx_bytes_per_sec"`
	NetTxBytesPerSec    *float64        `json:"net_tx_bytes_per_sec"`
	Pressure            *HostPressure   `json:"pressure"`
	Process             HostProcessInfo `json:"process"`
	PCIGPUs             []PCIGPUDevice  `json:"pci_gpus"`
	PCIScanSupported    bool            `json:"pci_scan_supported"`
	NvidiaDriverVersion string          `json:"nvidia_driver_version"`
	Notes               []string        `json:"notes"`
}

// HostSource samples local machine vitals. Rates (CPU %, network bytes/s) are computed between consecutive calls.
type HostSource struct {
	mu        sync.Mutex
	startedAt time.Time
	prev      hostCounters
	static    *hostStatic
}

type hostCounters struct {
	at      time.Time
	valid   bool
	cpuBusy uint64
	cpuIO   uint64
	cpuAll  uint64
	netOK   bool
	netRx   uint64
	netTx   uint64
}

type hostStatic struct {
	platform    string
	kernel      string
	cpuModel    string
	inContainer bool
}

// NewHostSource constructs the local host vitals sampler.
func NewHostSource() *HostSource {
	return &HostSource{startedAt: time.Now()}
}

func (hs *HostSource) ID() string   { return "host" }
func (hs *HostSource) Name() string { return "Host vitals (machine running GPU Lens)" }

func f64(v float64) *float64 { return &v }

// Collect returns the current host vitals.
func (hs *HostSource) Collect() *HostVitals {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	hostname, _ := os.Hostname()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	v := &HostVitals{
		CollectedAt: time.Now().UTC(),
		Hostname:    hostname,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		LogicalCPUs: runtime.NumCPU(),
		PCIGPUs:     []PCIGPUDevice{},
		Notes:       []string{},
		Process: HostProcessInfo{
			PID:           os.Getpid(),
			UptimeSeconds: time.Since(hs.startedAt).Seconds(),
			MemoryMiB:     float64(mem.Sys) / (1024 * 1024),
			Workers:       runtime.NumGoroutine(),
			Runtime:       "go " + runtime.Version(),
		},
	}
	hs.collectPlatform(v)
	return v
}
