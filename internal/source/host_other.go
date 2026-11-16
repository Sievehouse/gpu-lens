//go:build !linux

package source

// collectPlatform on non-Linux systems reports only the portable facts gathered by Collect. CPU, memory, disk,
// network and PCI readings are implemented for Linux (/proc and /sys); elsewhere they stay nil instead of being
// estimated.
func (hs *HostSource) collectPlatform(v *HostVitals) {
	v.Supported = false
	v.Notes = append(v.Notes, "Detailed host vitals (CPU, memory, disk, network, PCI inventory) are collected on Linux only. GPU telemetry sources are unaffected.")
}
