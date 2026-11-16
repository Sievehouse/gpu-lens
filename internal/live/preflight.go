package live

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/source"
)

type checkBuilder struct {
	groups []CheckGroup
}

func (b *checkBuilder) group(id, title string) {
	b.groups = append(b.groups, CheckGroup{ID: id, Title: title, Checks: []Check{}})
}

func (b *checkBuilder) add(id, title, status, detail, hint string, latency *float64) {
	g := &b.groups[len(b.groups)-1]
	g.Checks = append(g.Checks, Check{ID: id, Title: title, Status: status, Detail: detail, Hint: hint, LatencyMs: latency})
}

// sourceGate reports whether checks that depend on a source can be evaluated, and adds a single explanatory
// line when they cannot (source disabled, still being checked, not found, or failing).
func (b *checkBuilder) sourceGate(st *sourceState, id, title string) bool {
	s := st.status
	switch s.State {
	case StateDisabled:
		b.add(id, title, CheckSkip, "Turned off in the live configuration.", "Enable it under Connections.", nil)
		return false
	case StatePending:
		b.add(id, title, CheckPending, "Checking…", "", nil)
		return false
	case StateNotFound:
		b.add(id, title, CheckSkip, s.Detail, s.Hint, s.LastLatencyMs)
		return false
	case StateUnreachable, StateError:
		detail := s.Detail
		if s.LastError != "" {
			detail = strings.TrimSpace(detail + " " + s.LastError)
		}
		b.add(id, title, CheckFail, detail, s.Hint, s.LastLatencyMs)
		return false
	}
	return true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func emulatedSuffix(emulated bool) string {
	if emulated {
		return " This endpoint is the GPU Lens Live Lab emulator: values are synthetic."
	}
	return ""
}

// preflightLocked builds the live checklist from the current collector state. It performs no I/O.
func (e *Engine) preflightLocked(now time.Time) PreflightReport {
	b := &checkBuilder{}
	f := e.merge

	// --- GPU Lens core -----------------------------------------------------------------------------
	b.group("core", "GPU Lens core")
	b.add("core.api", "API server", CheckPass, fmt.Sprintf("Answering — go runtime, version %s.", Version), "", nil)
	if len(e.overrides) == 0 {
		b.add("core.rules", "Rule engine", CheckPass, fmt.Sprintf("%d deterministic rules registered with default thresholds.", len(e.rules.Definitions(e.thresholds))), "", nil)
	} else {
		keys := sortedKeys(e.overrides)
		b.add("core.rules", "Rule engine", CheckInfo,
			fmt.Sprintf("%d deterministic rules registered; %s active: %s.", len(e.rules.Definitions(e.thresholds)), plural(len(keys), "live threshold override", "live threshold overrides"), strings.Join(keys, ", ")),
			"Reset them under Rules & Thresholds to return to the defaults.", nil)
	}
	if e.guard.OK() {
		b.add("core.readonly_guard", "Read-only transport guard", CheckPass,
			fmt.Sprintf("%s rejected before any network I/O (%d network calls during the self-test).", strings.Join(e.guard.BlockedMethods, ", "), e.guard.NetworkCalls), "", nil)
	} else {
		b.add("core.readonly_guard", "Read-only transport guard", CheckFail,
			fmt.Sprintf("Self-test failed: %s were not blocked.", strings.Join(e.guard.LeakedMethods, ", ")),
			"Do not connect this build to production systems; the read-only guarantee is broken.", nil)
	}
	switch {
	case !e.running:
		b.add("core.collector", "Collector loop", CheckSkip, "Live collection is not running.", "Press Go Live.", nil)
	case e.tick == 0:
		b.add("core.collector", "Collector loop", CheckPending, "First collection cycle in progress…", "", nil)
	default:
		ms := float64(e.lastTickDur.Microseconds()) / 1000.0
		interval := e.effectiveInterval()
		status, hint := CheckPass, ""
		if e.lastTickDur > interval+500*time.Millisecond {
			status, hint = CheckWarn, "Collection takes longer than the refresh interval; choose a longer interval."
		}
		b.add("core.collector", "Collector loop", status,
			fmt.Sprintf("Cycle %d completed in %.0f ms; refreshing every %gs.", e.tick, ms, e.cfg.IntervalSeconds), hint, &ms)
	}
	if st := e.src[SourcePrometheus]; st != nil && st.prom != nil && st.prom.SkewSeconds != nil && (st.status.State == StateConnected || st.status.State == StateDegraded) {
		skew := *st.prom.SkewSeconds
		abs := math.Abs(skew)
		status, hint := CheckPass, ""
		if abs >= 30 {
			status, hint = CheckFail, "Staleness is computed from timestamps; synchronise clocks (NTP/chrony) on this host and on Prometheus."
		} else if abs >= 5 {
			status, hint = CheckWarn, "Clock skew distorts staleness figures; check NTP/chrony."
		}
		b.add("core.clock", "Clock agreement with Prometheus", status, fmt.Sprintf("This host is %+.1fs relative to the Prometheus server clock.", skew), hint, nil)
	} else {
		b.add("core.clock", "Clock agreement with Prometheus", CheckSkip, "Needs a connected Prometheus to compare clocks.", "", nil)
	}

	// --- Host --------------------------------------------------------------------------------------
	b.group("host", "Host running GPU Lens")
	hostSt := e.src[SourceHost]
	v := e.hostVitals
	if b.sourceGate(hostSt, "host.vitals", "Host vitals") {
		if v == nil {
			b.add("host.vitals", "Host vitals", CheckPending, "Checking…", "", nil)
		} else if !v.Supported {
			b.add("host.vitals", "Host vitals", CheckInfo, fmt.Sprintf("%s · %s/%s · %s.", v.Hostname, v.OS, v.Arch, plural(v.LogicalCPUs, "logical CPU", "logical CPUs")),
				"Detailed CPU, memory, disk and network vitals are collected on Linux hosts.", hostSt.status.LastLatencyMs)
			b.add("host.pressure", "Host resource pressure", CheckSkip, "Not measured on this operating system.", "", nil)
			b.add("host.pci", "GPU hardware on the PCI bus", CheckSkip, "The PCI scan is implemented for Linux (/sys).", "", nil)
		} else {
			b.add("host.vitals", "Host vitals", CheckPass,
				fmt.Sprintf("%s · %s · CPU %s · memory %s used.", v.Hostname, plural(v.LogicalCPUs, "logical CPU", "logical CPUs"), fmtPct(v.CPUPercent), fmtPct(v.MemUsedPercent)),
				"", hostSt.status.LastLatencyMs)
			var pressure []string
			if v.CPUPercent != nil && *v.CPUPercent >= 90 {
				pressure = append(pressure, fmt.Sprintf("CPU at %.0f%%", *v.CPUPercent))
			}
			if v.CPUIOWaitPercent != nil && *v.CPUIOWaitPercent >= 25 {
				pressure = append(pressure, fmt.Sprintf("I/O wait at %.0f%%", *v.CPUIOWaitPercent))
			}
			if v.MemUsedPercent != nil && *v.MemUsedPercent >= 92 {
				pressure = append(pressure, fmt.Sprintf("memory %.0f%% used", *v.MemUsedPercent))
			}
			if v.Pressure != nil {
				if p := v.Pressure.IOSomeAvg10; p != nil && *p >= 20 {
					pressure = append(pressure, fmt.Sprintf("I/O pressure %.0f%%", *p))
				}
				if p := v.Pressure.MemorySomeAvg10; p != nil && *p >= 20 {
					pressure = append(pressure, fmt.Sprintf("memory pressure %.0f%%", *p))
				}
			}
			if len(pressure) > 0 {
				b.add("host.pressure", "Host resource pressure", CheckWarn, "High: "+strings.Join(pressure, ", ")+".",
					"CPU, memory or storage starvation on the host slows data loaders and depresses GPU utilization.", nil)
			} else {
				b.add("host.pressure", "Host resource pressure", CheckPass, "CPU, memory and I/O are not saturated.", "", nil)
			}
			e.addPCICheck(b, v)
		}
	} else {
		b.add("host.pressure", "Host resource pressure", CheckSkip, "Host vitals are not being collected.", "", nil)
		b.add("host.pci", "GPU hardware on the PCI bus", CheckSkip, "Host vitals are not being collected.", "", nil)
	}

	// --- Local NVIDIA driver -----------------------------------------------------------------------
	b.group("nvidia", "Local NVIDIA driver (nvidia-smi)")
	smiSt := e.src[SourceNvidiaSMI]
	if b.sourceGate(smiSt, "nvsmi.query", "Driver query") {
		res := smiSt.smi
		if res == nil {
			b.add("nvsmi.query", "Driver query", CheckPending, "Checking…", "", nil)
		} else {
			status := CheckPass
			if smiSt.status.State == StateDegraded {
				status = CheckWarn
			}
			b.add("nvsmi.query", "Driver query", status,
				fmt.Sprintf("%s — %s (%s).", smiSt.status.Detail, res.BinaryPath, res.FieldsNote), smiSt.status.Hint, smiSt.status.LastLatencyMs)
			procs, busy := 0, 0
			for _, list := range res.Processes {
				procs += len(list)
				if len(list) > 0 {
					busy++
				}
			}
			if procs == 0 {
				b.add("nvsmi.processes", "Compute processes", CheckInfo, "No compute process currently holds a GPU context.", "", nil)
			} else {
				b.add("nvsmi.processes", "Compute processes", CheckPass, fmt.Sprintf("%s on %s.", plural(procs, "compute process", "compute processes"), plural(busy, "GPU", "GPUs")), "", nil)
			}
			if f.smiOnlyGPUs > 0 {
				b.add("nvsmi.coverage", "Hardware-fault signal coverage", CheckWarn,
					fmt.Sprintf("%s read only through nvidia-smi: Xid codes, PCIe replay, NVLink and thermal/power violation counters are not available from it.", plural(f.smiOnlyGPUs, "GPU is", "GPUs are")),
					"Connect a DCGM exporter for full hardware-health and failure-correlation coverage.", nil)
			} else if len(res.GPUs) > 0 {
				b.add("nvsmi.coverage", "Hardware-fault signal coverage", CheckPass, "Local GPUs are also covered by DCGM telemetry, so fault counters are available.", "", nil)
			}
		}
	}

	// --- DCGM exporter -----------------------------------------------------------------------------
	b.group("dcgm", "DCGM exporter")
	dcgmSt := e.src[SourceDCGM]
	if b.sourceGate(dcgmSt, "dcgm.reachable", "Endpoint reachable") {
		n := dcgmSt.dcgmNorm
		if n == nil || dcgmSt.dcgm == nil {
			b.add("dcgm.reachable", "Endpoint reachable", CheckPending, "Scrape answered; reading series…", "", dcgmSt.status.LastLatencyMs)
		} else {
			b.add("dcgm.reachable", "Endpoint reachable", CheckPass,
				fmt.Sprintf("%s answered with %s.%s", dcgmSt.status.Endpoint, plural(n.SeriesCount, "series", "series"), emulatedSuffix(dcgmSt.status.Emulated)),
				"", dcgmSt.status.LastLatencyMs)
			e.addMetricChecks(b, "dcgm", n)
			nodes := map[string]bool{}
			for _, g := range n.GPUs {
				nodes[g.NodeName] = true
			}
			if len(n.GPUs) == 0 {
				b.add("dcgm.gpus", "GPU devices", CheckWarn, "No series carries a GPU UUID label.", "Confirm this is a dcgm-exporter endpoint and that the exporter can see GPUs.", nil)
			} else {
				detail := fmt.Sprintf("%s on %s.", plural(len(n.GPUs), "GPU", "GPUs"), plural(len(nodes), "node", "nodes"))
				if n.MIGInstances > 0 {
					detail += fmt.Sprintf(" %s shown as separate devices.", plural(n.MIGInstances, "MIG instance", "MIG instances"))
				}
				b.add("dcgm.gpus", "GPU devices", CheckPass, detail, "", nil)
			}
			e.addAttributionCheck(b, "dcgm.attribution", n)
		}
	}

	// --- Prometheus --------------------------------------------------------------------------------
	b.group("prometheus", "Prometheus")
	promSt := e.src[SourcePrometheus]
	if b.sourceGate(promSt, "prom.reachable", "API reachable") {
		res := promSt.prom
		if res == nil {
			b.add("prom.reachable", "API reachable", CheckPending, "Checking…", "", nil)
		} else {
			n := res.Normalized
			ver := "version not reported"
			if res.Version != "" {
				ver = "version " + res.Version
			}
			b.add("prom.reachable", "API reachable", CheckPass,
				fmt.Sprintf("%s answered (%s).%s", promSt.status.Endpoint, ver, emulatedSuffix(promSt.status.Emulated)), "", promSt.status.LastLatencyMs)
			if len(n.GPUs) == 0 {
				b.add("prom.series", "DCGM series", CheckWarn, "Connected, but no DCGM GPU series were found.",
					"Make sure Prometheus scrapes dcgm-exporter (ServiceMonitor or scrape job on port 9400).", nil)
			} else {
				nodes := map[string]bool{}
				for _, g := range n.GPUs {
					nodes[g.NodeName] = true
				}
				b.add("prom.series", "DCGM series", CheckPass, fmt.Sprintf("%s on %s visible.", plural(len(n.GPUs), "GPU", "GPUs"), plural(len(nodes), "node", "nodes")), "", nil)
				e.addMetricChecks(b, "prom", &n)
				e.addAttributionCheck(b, "prom.attribution", &n)
			}
			switch {
			case !res.TargetsKnown:
				b.add("prom.targets", "GPU scrape targets", CheckSkip, "No scrape job with dcgm, nvidia or gpu in its name was found in `up`.", "", nil)
			case res.TargetsDown > 0:
				b.add("prom.targets", "GPU scrape targets", CheckWarn,
					fmt.Sprintf("%d up, %d down: %s.", res.TargetsUp, res.TargetsDown, strings.Join(res.DownTargets, ", ")),
					"A down exporter target is a telemetry blind spot for every GPU on that node.", nil)
			default:
				b.add("prom.targets", "GPU scrape targets", CheckPass, fmt.Sprintf("%s up.", plural(res.TargetsUp, "target", "targets")), "", nil)
			}
			if len(n.GPUs) > 0 {
				oldest := 0.0
				for _, g := range n.GPUs {
					if age := now.Sub(g.LastSampleAt).Seconds(); age > oldest {
						oldest = age
					}
				}
				status, hint := CheckPass, ""
				if oldest > e.thresholds.MaxStalenessSeconds {
					status, hint = CheckFail, "Samples are older than the staleness limit; check the exporter and the scrape job."
				} else if oldest > 90 {
					status, hint = CheckWarn, "Samples are getting old; check the scrape interval."
				}
				basis := "evaluation time (sample timestamps unavailable)"
				if res.TimestampsKnown {
					basis = "real scrape timestamps"
				}
				b.add("prom.freshness", "Sample freshness", status, fmt.Sprintf("Oldest GPU sample is %.0fs old, measured from %s.", oldest, basis), hint, nil)
			}
			switch {
			case e.cfg.PrometheusBackfillMinutes <= 0:
				b.add("prom.backfill", "History backfill", CheckInfo, "Turned off.", "", nil)
			case !e.backfill.attempted:
				b.add("prom.backfill", "History backfill", CheckPending, "Waiting for the first GPU series.", "", nil)
			case !e.backfill.ok:
				b.add("prom.backfill", "History backfill", CheckWarn, "Range query failed: "+e.backfill.detail,
					"Utilization rules will build their observation window from live samples instead.", nil)
			case e.backfill.devices == 0:
				b.add("prom.backfill", "History backfill", CheckInfo, "The range query returned no earlier samples.", "", nil)
			default:
				b.add("prom.backfill", "History backfill", CheckPass,
					fmt.Sprintf("Loaded %.0f minutes of history for %s.", e.backfill.minutes, plural(e.backfill.devices, "GPU", "GPUs")), "", nil)
			}
		}
	}

	// --- Kubernetes --------------------------------------------------------------------------------
	b.group("kubernetes", "Kubernetes API")
	k8sSt := e.src[SourceKubernetes]
	if b.sourceGate(k8sSt, "k8s.reachable", "API reachable") {
		res := k8sSt.k8s
		if res == nil {
			b.add("k8s.reachable", "API reachable", CheckPending, "Checking…", "", nil)
		} else {
			b.add("k8s.reachable", "API reachable", CheckPass,
				fmt.Sprintf("%s answered (%s).%s", k8sSt.status.Endpoint, orDash(res.Version), emulatedSuffix(res.Emulated)), "", k8sSt.status.LastLatencyMs)
			for _, a := range res.Access {
				id := "k8s." + a.Resource
				title := "List " + a.Resource
				if !a.OK {
					status := CheckFail
					hint := "Grant get/list on " + a.Resource + " (deploy/kubernetes/rbac-readonly.yaml)."
					if a.Resource == "events" {
						status = CheckWarn
						hint += " Without events the scheduling rule loses FailedScheduling detail."
					}
					detail := fmt.Sprintf("Denied with HTTP %d.", a.Status)
					if a.Status == 0 {
						detail = "Request failed: " + a.Error
					}
					b.add(id, title, status, detail, hint, nil)
					continue
				}
				detail := ""
				switch a.Resource {
				case "nodes":
					gpus := 0
					for _, n := range res.Nodes {
						gpus += n.GPUCapacity
					}
					detail = fmt.Sprintf("%s, %d advertising nvidia.com/gpu (%s).", plural(res.TotalNodes, "node", "nodes"), res.GPUNodes, plural(gpus, "GPU", "GPUs"))
				case "pods":
					detail = fmt.Sprintf("%s scanned, %d requesting GPUs.", plural(res.TotalPodsSeen, "pod", "pods"), len(res.Pods))
				case "events":
					detail = fmt.Sprintf("%s in the event window.", plural(len(res.Events), "warning event", "warning events"))
				}
				if a.Truncated {
					detail += " List was truncated at the page limit."
				}
				b.add(id, title, CheckPass, detail, "", nil)
			}
			allowed, blocked := int64(0), int64(0)
			if e.k8sSrc != nil {
				allowed, blocked = e.k8sSrc.Guard().Stats()
			}
			b.add("k8s.readonly", "Read-only access", CheckPass,
				fmt.Sprintf("%d GET requests sent, %d mutating requests attempted. GPU Lens never creates, patches or deletes cluster objects.", allowed, blocked), "", nil)
			nodesListed := false
			for _, a := range res.Access {
				if a.Resource == "nodes" && a.OK {
					nodesListed = true
				}
			}
			if nodesListed {
				if res.GPUNodes == 0 {
					b.add("k8s.device_plugin", "NVIDIA device plugin", CheckWarn, "No node advertises nvidia.com/gpu capacity.",
						"Install the NVIDIA device plugin or GPU Operator, or check that GPU nodes have joined.", nil)
				} else if len(res.OtherResources) > 0 {
					b.add("k8s.device_plugin", "NVIDIA device plugin", CheckInfo,
						fmt.Sprintf("%d GPU node(s). Also present but not modeled: %s.", res.GPUNodes, strings.Join(res.OtherResources, ", ")),
						"MIG slices and shared-GPU resources are outside this release's capacity math.", nil)
				} else {
					b.add("k8s.device_plugin", "NVIDIA device plugin", CheckPass, fmt.Sprintf("%s advertising nvidia.com/gpu.", plural(res.GPUNodes, "node is", "nodes are")), "", nil)
				}
			}
		}
	}

	// --- Cross-source correlation ------------------------------------------------------------------
	b.group("correlation", "Cross-source correlation")
	switch {
	case e.tick == 0:
		b.add("corr.telemetry", "GPU telemetry coverage", CheckPending, "Waiting for the first collection cycle.", "", nil)
	case f.totalGPUs == 0:
		hint := "Install the NVIDIA driver on this machine, start a DCGM exporter, or connect Prometheus under Connections. To try the pipeline without GPU hardware, run `python3 scripts/live_lab.py`."
		b.add("corr.telemetry", "GPU telemetry coverage", CheckFail, "No GPU is reported by any connected source.", hint, nil)
	default:
		detail := fmt.Sprintf("%s observed, %d fresh.", plural(f.totalGPUs, "GPU", "GPUs"), f.freshGPUs)
		status, hint := CheckPass, ""
		if f.freshGPUs < f.totalGPUs {
			status, hint = CheckWarn, "Stale GPUs are excluded from health verdicts and raise a data-freshness incident."
		}
		if f.emulatedGPUs > 0 {
			detail += fmt.Sprintf(" %d come from the Live Lab emulator (synthetic).", f.emulatedGPUs)
		}
		b.add("corr.telemetry", "GPU telemetry coverage", status, detail, hint, nil)
	}
	if e.k8sNodesOK && f.totalGPUs > 0 {
		if len(f.unmatchedNodes) > 0 {
			names := append([]string(nil), f.unmatchedNodes...)
			sort.Strings(names)
			b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", CheckWarn,
				fmt.Sprintf("GPU series name %s unknown to Kubernetes: %s.", plural(len(names), "a node", "nodes"), strings.Join(names, ", ")),
				"Set NODE_NAME on the dcgm-exporter pods or relabel a `node` label so telemetry and node inventory can be joined.", nil)
		} else {
			b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", CheckPass, "Every cluster GPU series maps to a known Kubernetes node.", "", nil)
		}
	} else {
		b.add("corr.nodes", "Telemetry ↔ Kubernetes node names", CheckSkip, "Needs both a Kubernetes connection and cluster GPU telemetry.", "", nil)
	}
	switch {
	case e.k8sPodsOK && f.totalGPUs > 0:
		var problems []string
		problems = append(problems, f.attributionGaps...)
		if f.unknownPodGPUs > 0 {
			problems = append(problems, fmt.Sprintf("%s carry pod labels for pods the API does not list", plural(f.unknownPodGPUs, "GPU", "GPUs")))
		}
		if len(problems) > 0 {
			b.add("corr.attribution", "Pod ↔ GPU attribution", CheckWarn, strings.Join(problems, "; ")+".",
				"Run dcgm-exporter with Kubernetes pod-resources mapping enabled so each allocated GPU carries pod labels.", nil)
		} else {
			b.add("corr.attribution", "Pod ↔ GPU attribution", CheckPass,
				fmt.Sprintf("%d of %d GPUs are attributed to workloads; Kubernetes reports %d GPUs allocated.", f.mappedGPUs, f.totalGPUs, f.k8sAllocatedGPUs), "", nil)
		}
	case f.totalGPUs > 0 && f.hostWorkloads > 0:
		b.add("corr.attribution", "Pod ↔ GPU attribution", CheckInfo,
			fmt.Sprintf("No Kubernetes on this host: %s attributed to compute processes instead of pods.", plural(f.mappedGPUs, "GPU is", "GPUs are")), "", nil)
	case f.totalGPUs > 0 && f.inferredPods > 0:
		b.add("corr.attribution", "Pod ↔ GPU attribution", CheckInfo,
			fmt.Sprintf("%s inferred from DCGM pod labels; pod phase, restarts and scheduling state need the Kubernetes API.", plural(f.inferredPods, "workload", "workloads")),
			"Connect the Kubernetes API for scheduling and failure-correlation rules.", nil)
	default:
		b.add("corr.attribution", "Pod ↔ GPU attribution", CheckSkip, "Needs GPU telemetry with workload labels or a Kubernetes connection.", "", nil)
	}

	// --- Verdict -----------------------------------------------------------------------------------
	summary := map[string]int{CheckPass: 0, CheckWarn: 0, CheckFail: 0, CheckSkip: 0, CheckInfo: 0, CheckPending: 0}
	for _, g := range b.groups {
		for _, c := range g.Checks {
			summary[c.Status]++
		}
	}
	report := PreflightReport{GeneratedAt: now, Summary: summary, Groups: b.groups}
	switch {
	case !e.running && e.tick == 0:
		report.Verdict = "idle"
		report.VerdictText = "Press Go Live to check every source."
	case e.tick == 0 || summary[CheckPending] > 0:
		report.Verdict = "checking"
		report.VerdictText = "Checking every source…"
	case f.totalGPUs == 0 && !e.k8sNodesOK:
		report.Verdict = "blocked"
		report.VerdictText = "No GPU telemetry source is reachable from this machine. The hints below show how to connect one."
	case summary[CheckFail] > 0 || summary[CheckWarn] > 0:
		report.Verdict = "degraded"
		report.VerdictText = fmt.Sprintf("Live with caveats: %d failing and %d warning check(s).", summary[CheckFail], summary[CheckWarn])
	default:
		report.Verdict = "ready"
		report.VerdictText = fmt.Sprintf("All checks passed — %s monitored live.", plural(f.totalGPUs, "GPU", "GPUs"))
	}
	return report
}

func (e *Engine) addPCICheck(b *checkBuilder, v *source.HostVitals) {
	if !v.PCIScanSupported {
		b.add("host.pci", "GPU hardware on the PCI bus", CheckSkip, "The PCI bus is not readable here (/sys/bus/pci).", "", nil)
		return
	}
	if len(v.PCIGPUs) == 0 {
		b.add("host.pci", "GPU hardware on the PCI bus", CheckInfo, "No display-class PCI device is present on this machine.",
			"This is normal for a VM or a control-plane host; point GPU Lens at a DCGM exporter or Prometheus instead.", nil)
		return
	}
	byVendor := map[string]int{}
	var unbound []string
	for _, d := range v.PCIGPUs {
		byVendor[d.Vendor]++
		if d.VendorID == "10de" && d.Driver != "nvidia" {
			drv := d.Driver
			if drv == "" {
				drv = "no driver"
			}
			unbound = append(unbound, fmt.Sprintf("%s (%s)", d.Address, drv))
		}
	}
	var parts []string
	for _, vendor := range sortedKeys(byVendor) {
		parts = append(parts, fmt.Sprintf("%d × %s", byVendor[vendor], vendor))
	}
	detail := strings.Join(parts, ", ") + "."
	if v.NvidiaDriverVersion != "" {
		detail += " NVIDIA kernel driver " + v.NvidiaDriverVersion + " is loaded."
	}
	switch {
	case len(unbound) > 0:
		b.add("host.pci", "GPU hardware on the PCI bus", CheckWarn, detail+" NVIDIA device not bound to the nvidia driver: "+strings.Join(unbound, ", ")+".",
			"Install or load the proprietary NVIDIA driver; without it there is no telemetry for these GPUs.", nil)
	case byVendor["NVIDIA"] == 0:
		b.add("host.pci", "GPU hardware on the PCI bus", CheckInfo, detail+" None is an NVIDIA device, so no NVML telemetry is available locally.", "", nil)
	default:
		b.add("host.pci", "GPU hardware on the PCI bus", CheckPass, detail, "", nil)
	}
}

func (e *Engine) addMetricChecks(b *checkBuilder, prefix string, n *source.LiveNormalizeResult) {
	req, missingReq := source.MissingMetrics(source.RequiredDCGMMetrics(e.base.MetricMappings), n.Exported)
	if len(missingReq) == 0 {
		b.add(prefix+".metrics", "Default DCGM metrics", CheckPass, fmt.Sprintf("All %d default metrics are exported.", req), "", nil)
	} else {
		b.add(prefix+".metrics", "Default DCGM metrics", CheckWarn,
			fmt.Sprintf("%d of %d exported; missing: %s.", req, req+len(missingReq), strings.Join(missingReq, ", ")),
			"Rules that need a missing metric treat it as not exported — never as zero. Enable it in the dcgm-exporter collectors CSV.", nil)
	}
	opt, missingOpt := source.MissingMetrics(source.OptionalDCGMMetrics(e.base.MetricMappings), n.Exported)
	if len(missingOpt) == 0 {
		b.add(prefix+".optional", "Optional fault metrics", CheckPass, fmt.Sprintf("All %d optional fault metrics are exported.", opt), "", nil)
	} else {
		b.add(prefix+".optional", "Optional fault metrics", CheckInfo,
			fmt.Sprintf("%d of %d exported; not exported: %s.", opt, opt+len(missingOpt), strings.Join(missingOpt, ", ")),
			"Optional: uncomment these in the dcgm-exporter CSV for ECC, NVLink and violation coverage.", nil)
	}
}

func (e *Engine) addAttributionCheck(b *checkBuilder, id string, n *source.LiveNormalizeResult) {
	if len(n.GPUs) == 0 {
		return
	}
	switch {
	case n.IgnoredSelfLabels > 0:
		b.add(id, "Workload labels", CheckWarn,
			fmt.Sprintf("%s carried pod labels that point at the dcgm-exporter pod itself and were ignored.", plural(n.IgnoredSelfLabels, "GPU", "GPUs")),
			"Prometheus is overwriting workload labels with scrape-target labels. Set honorLabels: true on the ServiceMonitor, or keep honor_labels=false and GPU Lens will read exported_pod.", nil)
	case n.MappedGPUs == 0:
		b.add(id, "Workload labels", CheckInfo, "No GPU series carries pod labels.",
			"Either no workload holds a GPU, or the exporter runs without Kubernetes pod mapping.", nil)
	default:
		detail := fmt.Sprintf("%d of %d GPUs carry pod, namespace and container labels.", n.MappedGPUs, len(n.GPUs))
		if n.HonorLabelsRenamed {
			detail += " Read from exported_pod / exported_namespace (honor_labels=false)."
		}
		b.add(id, "Workload labels", CheckPass, detail, "", nil)
	}
}
