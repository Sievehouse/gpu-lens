//go:build linux

package source

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readFileTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readCPUCounters parses the aggregate "cpu" line of /proc/stat.
func readCPUCounters() (busy, iowait, all uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var vals []uint64
		for _, s := range fields[1:] {
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return 0, 0, 0, false
			}
			vals = append(vals, n)
		}
		// user nice system idle iowait irq softirq steal [guest guest_nice]; guest time is already in user.
		limit := len(vals)
		if limit > 8 {
			limit = 8
		}
		var total uint64
		for _, n := range vals[:limit] {
			total += n
		}
		idle := vals[3]
		var io uint64
		if len(vals) > 4 {
			io = vals[4]
		}
		return total - idle - io, io, total, true
	}
	return 0, 0, 0, false
}

func readNetCounters() (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(fields[0], 10, 64)
		t, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
		ok = true
	}
	return rx, tx, ok
}

func readMeminfo() map[string]float64 {
	out := map[string]float64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) < 2 {
			continue
		}
		kb, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		out[strings.TrimSuffix(parts[0], ":")] = kb / 1024.0
	}
	return out
}

var psiAvg10 = regexp.MustCompile(`avg10=([0-9.]+)`)

func readPressure(resource string) *float64 {
	b, err := os.ReadFile("/proc/pressure/" + resource)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		if m := psiAvg10.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				return &v
			}
		}
	}
	return nil
}

var pciVendorNames = map[string]string{
	"10de": "NVIDIA",
	"1002": "AMD",
	"8086": "Intel",
	"1a03": "ASPEED (BMC)",
	"102b": "Matrox",
	"15ad": "VMware",
	"1234": "QEMU/Bochs",
	"1af4": "Virtio",
	"1b36": "Red Hat (QXL)",
	"1414": "Microsoft (Hyper-V)",
}

var pciClassNames = map[string]string{
	"0300": "VGA controller",
	"0301": "XGA controller",
	"0302": "3D controller",
	"0380": "Display controller",
}

// scanPCIGPUs lists display-class PCI devices from sysfs. It only reads files; nothing is executed.
func scanPCIGPUs() ([]PCIGPUDevice, bool) {
	entries, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return []PCIGPUDevice{}, false
	}
	out := []PCIGPUDevice{}
	for _, e := range entries {
		base := filepath.Join("/sys/bus/pci/devices", e.Name())
		class := strings.TrimPrefix(readFileTrim(filepath.Join(base, "class")), "0x")
		if len(class) < 4 || class[:2] != "03" {
			continue
		}
		vendorID := strings.TrimPrefix(readFileTrim(filepath.Join(base, "vendor")), "0x")
		deviceID := strings.TrimPrefix(readFileTrim(filepath.Join(base, "device")), "0x")
		driver := ""
		if target, err := os.Readlink(filepath.Join(base, "driver")); err == nil {
			driver = filepath.Base(target)
		}
		vendor := pciVendorNames[vendorID]
		if vendor == "" {
			vendor = "Vendor 0x" + vendorID
		}
		className := pciClassNames[class[:4]]
		if className == "" {
			className = "Display device (class 0x" + class[:4] + ")"
		}
		out = append(out, PCIGPUDevice{
			Address:  e.Name(),
			Vendor:   vendor,
			VendorID: vendorID,
			DeviceID: deviceID,
			Class:    className,
			Driver:   driver,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, true
}

var nvidiaVersionPattern = regexp.MustCompile(`\b(\d{3}\.\d+(?:\.\d+)?)\b`)

func readNvidiaDriverVersion() string {
	text := readFileTrim("/proc/driver/nvidia/version")
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "NVRM version:") {
			if m := nvidiaVersionPattern.FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

func detectContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}
	cg := readFileTrim("/proc/1/cgroup")
	return strings.Contains(cg, "docker") || strings.Contains(cg, "kubepods") || strings.Contains(cg, "containerd")
}

func (hs *HostSource) loadStatic() *hostStatic {
	if hs.static != nil {
		return hs.static
	}
	st := &hostStatic{kernel: readFileTrim("/proc/sys/kernel/osrelease"), inContainer: detectContainer()}
	for _, line := range strings.Split(readFileTrim("/etc/os-release"), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			st.platform = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	if f, err := os.Open("/proc/cpuinfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") {
				if i := strings.IndexByte(line, ':'); i >= 0 {
					st.cpuModel = strings.TrimSpace(line[i+1:])
					break
				}
			}
		}
		f.Close()
	}
	hs.static = st
	return st
}

func (hs *HostSource) collectPlatform(v *HostVitals) {
	st := hs.loadStatic()
	v.Supported = true
	v.Platform, v.Kernel, v.CPUModel, v.InContainer = st.platform, st.kernel, st.cpuModel, st.inContainer
	if v.InContainer {
		v.Notes = append(v.Notes, "GPU Lens is running inside a container: host vitals reflect what the container can see.")
	}

	// CPU and network rates need two readings. On the very first call take a short second reading so the
	// console has a value immediately instead of waiting a full tick.
	busy, io, all, cpuOK := readCPUCounters()
	rx, tx, netOK := readNetCounters()
	now := time.Now()
	if !hs.prev.valid && cpuOK {
		hs.prev = hostCounters{at: now, valid: true, cpuBusy: busy, cpuIO: io, cpuAll: all, netOK: netOK, netRx: rx, netTx: tx}
		time.Sleep(150 * time.Millisecond)
		busy, io, all, cpuOK = readCPUCounters()
		rx, tx, netOK = readNetCounters()
		now = time.Now()
	}
	if cpuOK && hs.prev.valid && all > hs.prev.cpuAll {
		span := float64(all - hs.prev.cpuAll)
		v.CPUPercent = f64(clampPct(float64(busy-hs.prev.cpuBusy) / span * 100.0))
		v.CPUIOWaitPercent = f64(clampPct(float64(io-hs.prev.cpuIO) / span * 100.0))
	}
	if netOK && hs.prev.netOK {
		if secs := now.Sub(hs.prev.at).Seconds(); secs > 0 && rx >= hs.prev.netRx && tx >= hs.prev.netTx {
			v.NetRxBytesPerSec = f64(float64(rx-hs.prev.netRx) / secs)
			v.NetTxBytesPerSec = f64(float64(tx-hs.prev.netTx) / secs)
		}
	}
	if cpuOK {
		hs.prev = hostCounters{at: now, valid: true, cpuBusy: busy, cpuIO: io, cpuAll: all, netOK: netOK, netRx: rx, netTx: tx}
	}

	if parts := strings.Fields(readFileTrim("/proc/loadavg")); len(parts) >= 3 {
		if a, err := strconv.ParseFloat(parts[0], 64); err == nil {
			v.Load1 = &a
		}
		if b, err := strconv.ParseFloat(parts[1], 64); err == nil {
			v.Load5 = &b
		}
		if c, err := strconv.ParseFloat(parts[2], 64); err == nil {
			v.Load15 = &c
		}
	}
	if parts := strings.Fields(readFileTrim("/proc/uptime")); len(parts) >= 1 {
		if up, err := strconv.ParseFloat(parts[0], 64); err == nil {
			v.UptimeSeconds = &up
		}
	}

	mem := readMeminfo()
	if total, ok := mem["MemTotal"]; ok && total > 0 {
		v.MemTotalMiB = f64(total)
		if avail, ok := mem["MemAvailable"]; ok {
			v.MemAvailableMiB = f64(avail)
			v.MemUsedPercent = f64(clampPct((total - avail) / total * 100.0))
		}
	}
	if swapTotal, ok := mem["SwapTotal"]; ok {
		v.SwapTotalMiB = f64(swapTotal)
		if swapFree, ok := mem["SwapFree"]; ok {
			v.SwapUsedMiB = f64(swapTotal - swapFree)
		}
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil && fs.Blocks > 0 {
		total := float64(fs.Blocks) * float64(fs.Bsize)
		free := float64(fs.Bavail) * float64(fs.Bsize)
		v.DiskPath = "/"
		v.DiskTotalGiB = f64(total / (1024 * 1024 * 1024))
		v.DiskUsedPercent = f64(clampPct((total - free) / total * 100.0))
	}

	cpuP, memP, ioP := readPressure("cpu"), readPressure("memory"), readPressure("io")
	if cpuP != nil || memP != nil || ioP != nil {
		v.Pressure = &HostPressure{CPUSomeAvg10: cpuP, MemorySomeAvg10: memP, IOSomeAvg10: ioP}
	}

	v.PCIGPUs, v.PCIScanSupported = scanPCIGPUs()
	v.NvidiaDriverVersion = readNvidiaDriverVersion()
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
