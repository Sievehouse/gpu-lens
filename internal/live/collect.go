package live

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/source"
)

// recordOutcomeLocked updates a source's connection status as soon as its collection attempt finishes, so the
// console can show each check completing instead of waiting for the whole cycle.
func (e *Engine) recordOutcomeLocked(out outcome) {
	st := e.src[out.id]
	if st == nil {
		return
	}
	st.inFlight = false
	period := e.refreshPeriodLocked(out.id)
	started := out.at.Add(-out.latency)
	st.nextDue = started.Add(period)

	ms := float64(out.latency.Microseconds()) / 1000.0
	st.status.LastLatencyMs = &ms
	st.status.LatencyHistoryMs = append(st.status.LatencyHistoryMs, ms)
	if len(st.status.LatencyHistoryMs) > latencyHistoryLen {
		st.status.LatencyHistoryMs = append([]float64(nil), st.status.LatencyHistoryMs[len(st.status.LatencyHistoryMs)-latencyHistoryLen:]...)
	}
	st.status.RefreshSeconds = period.Seconds()

	fail := func(state, detail, hint string, err error) {
		st.status.State = state
		st.status.Detail = detail
		st.status.Hint = hint
		st.status.LastError = sanitizeError(err)
		st.status.FailureCount++
		st.status.ConsecutiveFailures++
	}
	succeed := func() {
		at := out.at
		st.status.LastSuccessAt = &at
		st.status.LastError = ""
		st.status.Hint = ""
		st.status.SuccessCount++
		st.status.ConsecutiveFailures = 0
		st.everConnected = true
	}

	switch out.id {
	case SourceHost:
		succeed()
		e.hostVitals = out.host
		e.describeHostLocked(st, out.host)

	case SourceNvidiaSMI:
		switch {
		case out.notFound:
			st.nextDue = started.Add(maxDuration(period, notFoundRetry))
			fail(StateNotFound, "nvidia-smi is not installed on this machine.",
				"Install the NVIDIA driver (it ships nvidia-smi), or connect a DCGM exporter or Prometheus instead.", out.err)
			// "Not installed" is an inventory fact, not a failing source.
			st.status.FailureCount--
			st.status.ConsecutiveFailures = 0
			st.status.LastError = ""
		case out.err != nil:
			fail(StateError, "nvidia-smi is installed but the driver query failed.", smiErrorHint(out.err), out.err)
		default:
			succeed()
			st.smi = out.smi
			e.describeSMILocked(st, out.smi)
		}

	case SourceDCGM:
		if out.err != nil {
			e.failNetworkLocked(st, out, started, period, fail,
				"No DCGM exporter answered on the default local address.",
				"Run dcgm-exporter on this host, or set the exporter URL under Connections (GPULENS_DCGM_EXPORTER_URL).",
				"The configured DCGM exporter could not be scraped.",
				"Check that the URL ends in /metrics and that the exporter pod or service is reachable from this machine.")
			return
		}
		succeed()
		st.dcgm = out.dcgm
		st.status.State = StateConnected
		st.status.Detail = fmt.Sprintf("Scrape OK — %d series.", len(out.dcgm.Series))

	case SourcePrometheus:
		if out.err != nil {
			e.failNetworkLocked(st, out, started, period, fail,
				"No Prometheus answered on the default local address.",
				"Set the Prometheus URL under Connections (GPULENS_PROMETHEUS_URL) if your cluster has one.",
				"The configured Prometheus could not be queried.",
				"Check the URL, network path and bearer token. GPU Lens needs GET access to /api/v1/query.")
			return
		}
		succeed()
		st.prom = mergePromAudit(st.prom, out.prom, out.promAudit)
		if out.promAudit {
			st.lastAuditAt = started
		}
		e.describePrometheusLocked(st)
		if out.backfillOK || out.backfillEr != "" {
			e.backfill.attempted = true
			e.backfill.ok = out.backfillOK
			e.backfill.detail = out.backfillEr
		}

	case SourceKubernetes:
		if out.err != nil {
			e.failNetworkLocked(st, out, started, period, fail,
				"No Kubernetes API answered on the default local address (kubectl proxy).",
				"Run `kubectl proxy --port=8001` next to GPU Lens, or set the API server URL and token under Connections.",
				"The configured Kubernetes API server could not be reached.",
				"Check the API server URL, the CA bundle (GPULENS_KUBERNETES_CA_FILE) and the bearer token.")
			return
		}
		st.k8s = out.k8s
		e.describeKubernetesLocked(st, out.k8s, succeed, fail)
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// failNetworkLocked classifies a failed HTTP source. A default address nobody configured is "not found" (a neutral
// inventory fact) until it has answered once; a configured or previously connected source is a real failure.
func (e *Engine) failNetworkLocked(st *sourceState, out outcome, started time.Time, period time.Duration,
	fail func(state, detail, hint string, err error), autoDetail, autoHint, explicitDetail, explicitHint string) {
	if !st.status.Explicit && !st.everConnected {
		st.nextDue = started.Add(maxDuration(period, notFoundRetry))
		fail(StateNotFound, autoDetail, autoHint, out.err)
		st.status.FailureCount--
		st.status.ConsecutiveFailures = 0
		st.status.LastError = ""
		return
	}
	detail := explicitDetail
	if !st.status.Explicit {
		detail = "This source was connected earlier in the session and has stopped answering."
	}
	fail(StateUnreachable, detail, explicitHint, out.err)
}

func smiErrorHint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "couldn't communicate with the nvidia driver"):
		return "The NVIDIA kernel driver is not loaded. Check `dmesg | grep -i nvrm`, reinstall the driver, or reboot."
	case strings.Contains(msg, "version mismatch"):
		return "The driver and its user-space library differ in version — reboot after a driver update."
	case strings.Contains(msg, "insufficient permissions"):
		return "The user running GPU Lens may not query NVML. Run it as a user with access to /dev/nvidia*."
	case strings.Contains(msg, "timed out"):
		return "The driver did not answer in time. A GPU may have fallen off the bus; check `dmesg` for Xid 79."
	case strings.Contains(msg, "gpu is lost"):
		return "A GPU has fallen off the bus. A reboot is normally required to recover it."
	}
	return "Run `nvidia-smi` in a terminal on this machine to see the full driver error."
}

func fmtPct(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *v)
}

func (e *Engine) describeHostLocked(st *sourceState, v *source.HostVitals) {
	if v == nil {
		return
	}
	st.status.Facts = []Fact{{"Hostname", v.Hostname}, {"OS", strings.TrimSpace(v.OS + "/" + v.Arch)}}
	if v.Platform != "" {
		st.status.Facts = append(st.status.Facts, Fact{"Platform", v.Platform})
	}
	if v.Kernel != "" {
		st.status.Facts = append(st.status.Facts, Fact{"Kernel", v.Kernel})
	}
	if v.CPUModel != "" {
		st.status.Facts = append(st.status.Facts, Fact{"CPU", v.CPUModel})
	}
	st.status.Facts = append(st.status.Facts, Fact{"Logical CPUs", fmt.Sprintf("%d", v.LogicalCPUs)})
	if !v.Supported {
		st.status.State = StateDegraded
		st.status.Detail = fmt.Sprintf("Basic host facts only on %s (%d logical CPUs).", v.OS, v.LogicalCPUs)
		st.status.Hint = "Detailed CPU, memory, disk and network vitals are collected on Linux hosts."
		return
	}
	st.status.State = StateConnected
	st.status.Detail = fmt.Sprintf("%d logical CPUs · CPU %s · memory %s used", v.LogicalCPUs, fmtPct(v.CPUPercent), fmtPct(v.MemUsedPercent))
}

func (e *Engine) describeSMILocked(st *sourceState, res *source.NvidiaSMIResult) {
	st.status.Endpoint = "local " + res.BinaryPath
	procs := 0
	for _, list := range res.Processes {
		procs += len(list)
	}
	st.status.Facts = []Fact{
		{"Binary", res.BinaryPath},
		{"Driver version", orDash(res.DriverVersion)},
		{"GPUs", fmt.Sprintf("%d", len(res.GPUs))},
		{"Compute processes", fmt.Sprintf("%d", procs)},
		{"Query fields", res.FieldsNote},
	}
	switch {
	case res.NoDevices || len(res.GPUs) == 0:
		st.status.State = StateDegraded
		st.status.Detail = "The NVIDIA driver answered but reports no GPU devices."
		st.status.Hint = "Check that the GPU is seated and visible in `lspci`, and that the driver matches the hardware."
	case len(res.Notes) > 0:
		st.status.State = StateDegraded
		st.status.Detail = fmt.Sprintf("%d GPU(s) · driver %s — %s", len(res.GPUs), orDash(res.DriverVersion), res.Notes[0])
	default:
		st.status.State = StateConnected
		st.status.Detail = fmt.Sprintf("%d GPU(s) · driver %s · %d compute process(es)", len(res.GPUs), orDash(res.DriverVersion), procs)
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// mergePromAudit keeps the last audit facts (version, skew, target health) on ticks that skip the audit queries.
func mergePromAudit(prev, cur *source.PromLiveResult, audited bool) *source.PromLiveResult {
	if cur == nil || audited || prev == nil {
		return cur
	}
	cur.Version = prev.Version
	cur.SkewSeconds = prev.SkewSeconds
	cur.TargetsKnown, cur.TargetsUp, cur.TargetsDown, cur.DownTargets = prev.TargetsKnown, prev.TargetsUp, prev.TargetsDown, prev.DownTargets
	return cur
}

func (e *Engine) describePrometheusLocked(st *sourceState) {
	res := st.prom
	if res == nil {
		return
	}
	n := res.Normalized
	st.status.Emulated = n.Emulated
	nodes := map[string]bool{}
	for _, g := range n.GPUs {
		nodes[g.NodeName] = true
	}
	facts := []Fact{{"Version", orDash(res.Version)}, {"GPUs via DCGM series", fmt.Sprintf("%d on %d node(s)", len(n.GPUs), len(nodes))}}
	req, missingReq := source.MissingMetrics(source.RequiredDCGMMetrics(e.base.MetricMappings), n.Exported)
	facts = append(facts, Fact{"Default metrics", fmt.Sprintf("%d of %d", req, req+len(missingReq))})
	if res.SkewSeconds != nil {
		facts = append(facts, Fact{"Clock skew", fmt.Sprintf("%+.1fs", *res.SkewSeconds)})
	}
	if res.TargetsKnown {
		facts = append(facts, Fact{"GPU scrape targets", fmt.Sprintf("%d up, %d down", res.TargetsUp, res.TargetsDown)})
	}
	if n.HonorLabelsRenamed {
		facts = append(facts, Fact{"Workload labels", "exported_pod / exported_namespace (honor_labels=false)"})
	}
	st.status.Facts = facts
	switch {
	case len(n.GPUs) == 0:
		st.status.State = StateDegraded
		st.status.Detail = "Connected, but no DCGM GPU series were found."
		st.status.Hint = "Make sure Prometheus scrapes dcgm-exporter (a ServiceMonitor or scrape job for port 9400)."
	case len(missingReq) > 0:
		st.status.State = StateDegraded
		st.status.Detail = fmt.Sprintf("%d GPU(s) visible; %d default metric(s) missing: %s", len(n.GPUs), len(missingReq), strings.Join(missingReq, ", "))
		st.status.Hint = "Enable the missing fields in the dcgm-exporter collectors CSV."
	default:
		st.status.State = StateConnected
		st.status.Detail = fmt.Sprintf("%d GPU(s) on %d node(s) via DCGM series", len(n.GPUs), len(nodes))
	}
	if res.TargetsKnown && res.TargetsDown > 0 && st.status.State == StateConnected {
		st.status.State = StateDegraded
		st.status.Detail += fmt.Sprintf(" — %d scrape target(s) down", res.TargetsDown)
	}
}

func (e *Engine) describeDCGMLocked(st *sourceState) {
	n := st.dcgmNorm
	scrape := st.dcgm
	if n == nil || scrape == nil {
		return
	}
	st.status.Emulated = n.Emulated || scrape.EmulatorHeader != ""
	nodes := map[string]bool{}
	for _, g := range n.GPUs {
		nodes[g.NodeName] = true
	}
	req, missingReq := source.MissingMetrics(source.RequiredDCGMMetrics(e.base.MetricMappings), n.Exported)
	opt, missingOpt := source.MissingMetrics(source.OptionalDCGMMetrics(e.base.MetricMappings), n.Exported)
	st.status.Facts = []Fact{
		{"Series", fmt.Sprintf("%d (%d bytes)", n.SeriesCount, scrape.Bytes)},
		{"GPUs", fmt.Sprintf("%d on %d node(s)", len(n.GPUs), len(nodes))},
		{"Default metrics", fmt.Sprintf("%d of %d", req, req+len(missingReq))},
		{"Optional fault metrics", fmt.Sprintf("%d of %d", opt, opt+len(missingOpt))},
		{"GPUs with pod labels", fmt.Sprintf("%d", n.MappedGPUs)},
	}
	if scrape.SkippedLines > 0 {
		st.status.Facts = append(st.status.Facts, Fact{"Unparseable lines skipped", fmt.Sprintf("%d", scrape.SkippedLines)})
	}
	switch {
	case len(n.GPUs) == 0:
		st.status.State = StateDegraded
		st.status.Detail = "The endpoint answered but exposes no DCGM GPU series."
		st.status.Hint = "Confirm this URL is a dcgm-exporter /metrics endpoint and that the exporter can see GPUs."
	case len(missingReq) > 0:
		st.status.State = StateDegraded
		st.status.Detail = fmt.Sprintf("%d GPU(s); %d default metric(s) missing: %s", len(n.GPUs), len(missingReq), strings.Join(missingReq, ", "))
		st.status.Hint = "Enable the missing fields in the dcgm-exporter collectors CSV."
	default:
		st.status.State = StateConnected
		st.status.Detail = fmt.Sprintf("%d GPU(s) on %d node(s) · %d series", len(n.GPUs), len(nodes), n.SeriesCount)
	}
}

func (e *Engine) describeKubernetesLocked(st *sourceState, res *source.K8sLiveResult, succeed func(), fail func(state, detail, hint string, err error)) {
	st.status.Emulated = res.Emulated
	var denied []string
	facts := []Fact{{"Version", orDash(res.Version)}}
	for _, a := range res.Access {
		val := fmt.Sprintf("%d listed", a.Items)
		if !a.OK {
			val = fmt.Sprintf("denied (HTTP %d)", a.Status)
			if a.Status == 0 {
				val = "failed"
			}
			denied = append(denied, a.Resource)
		} else if a.Truncated {
			val += " (truncated)"
		}
		facts = append(facts, Fact{"GET " + a.Resource, val})
	}
	sort.Strings(denied)
	if !res.Usable() {
		st.status.Facts = facts
		if !st.status.Explicit && !st.everConnected {
			// Something listens on the default kubectl-proxy port, but it is not a usable Kubernetes API.
			st.nextDue = time.Now().Add(notFoundRetry)
			st.status.State = StateNotFound
			st.status.Detail = "The default local address answered, but not as a usable Kubernetes API."
			st.status.Hint = "Run `kubectl proxy --port=8001` next to GPU Lens, or set the API server URL and token under Connections."
			st.status.LastError = ""
			return
		}
		fail(StateError, "The API server answered, but nodes and pods could not be listed.",
			"Apply deploy/kubernetes/rbac-readonly.yaml and use that service account's token (HTTP 401 = bad token, 403 = missing RBAC).",
			errors.New("listing nodes and pods was denied"))
		return
	}
	succeed()
	gpuCap := 0
	for _, n := range res.Nodes {
		gpuCap += n.GPUCapacity
	}
	facts = append(facts,
		Fact{"GPU nodes", fmt.Sprintf("%d of %d (%d GPUs)", res.GPUNodes, res.TotalNodes, gpuCap)},
		Fact{"GPU pods", fmt.Sprintf("%d of %d scanned", len(res.Pods), res.TotalPodsSeen)},
		Fact{"Warning events", fmt.Sprintf("%d", len(res.Events))},
	)
	if len(res.OtherResources) > 0 {
		facts = append(facts, Fact{"Not modeled", strings.Join(res.OtherResources, ", ")})
	}
	st.status.Facts = facts
	if len(denied) > 0 {
		st.status.State = StateDegraded
		st.status.Detail = fmt.Sprintf("Connected (%s), but access is partial: %s denied.", orDash(res.Version), strings.Join(denied, ", "))
		st.status.Hint = "Grant get/list on nodes, pods and events (deploy/kubernetes/rbac-readonly.yaml)."
		return
	}
	st.status.State = StateConnected
	st.status.Detail = fmt.Sprintf("%s · %d node(s), %d with GPUs · %d GPU pod(s)", orDash(res.Version), res.TotalNodes, res.GPUNodes, len(res.Pods))
}
