// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package live

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/capacity"
	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
	"github.com/Sievehouse/gpu-lens/internal/source"
)

const (
	// HostProcessNamespace groups compute processes found on a standalone GPU host (no Kubernetes pod involved).
	HostProcessNamespace = "host-processes"

	inferredNodeLabel   = "gpulens.io/inferred"
	rawSampleWindow     = 15 * time.Minute
	sampleRetention     = 6 * time.Hour
	thinEveryTicks      = 30
	lowUtilRuleID       = "GPU-UTIL-003"
	noteHostProcess     = "Host compute process reported by the local GPU driver (nvidia-smi / amd-smi), not a Kubernetes pod."
	noteInferredFromDCG = "Inferred from DCGM pod labels (Kubernetes API not connected)."
)

// observation is the latest report of one device by one source.
type observation struct {
	dev      domain.GPUDevice
	extras   source.GPUExtras
	at       time.Time
	exported map[string]bool
}

// deviceState is everything the engine remembers about one GPU during a session.
type deviceState struct {
	obs        map[string]*observation
	dev        domain.GPUDevice
	extras     source.GPUExtras
	samples    []domain.GPUMetricPoint
	firstSeen  time.Time
	lastSeen   time.Time
	lastSample time.Time
	procPID    int
	procName   string
	procSince  time.Time
	hostProc   bool
}

// mergeFacts are cross-source findings produced while assembling a snapshot; the preflight report shows them.
type mergeFacts struct {
	totalGPUs        int
	freshGPUs        int
	mappedGPUs       int
	smiOnlyGPUs      int
	amdGPUs          int
	emulatedGPUs     int
	unmatchedNodes   []string
	attributionGaps  []string
	unknownPodGPUs   int
	inferredNodes    []string
	k8sAllocatedGPUs int
	k8sGPUCapacity   int
	hostWorkloads    int
	inferredPods     int
}

var sourcePriority = map[string]int{SourceNvidiaSMI: 0, SourceAMDSMI: 0, SourceDCGM: 1, SourcePrometheus: 2}

// isLocalDriverSource reports whether a source reads the GPUs of the machine GPU Lens runs on through its driver
// tool (nvidia-smi or amd-smi), as opposed to cluster telemetry.
func isLocalDriverSource(id string) bool { return id == SourceNvidiaSMI || id == SourceAMDSMI }

func cloneExtras(ex source.GPUExtras) source.GPUExtras {
	out := ex
	out.Sources = append([]string(nil), ex.Sources...)
	out.ThrottleReasons = append([]string{}, ex.ThrottleReasons...)
	out.Processes = append([]source.GPUProcess{}, ex.Processes...)
	if ex.PCIe != nil {
		p := *ex.PCIe
		out.PCIe = &p
	}
	if ex.FanSpeedPercent != nil {
		f := *ex.FanSpeedPercent
		out.FanSpeedPercent = &f
	}
	return out
}

func (e *Engine) observeLocked(sourceID string, dev domain.GPUDevice, extras *source.GPUExtras, exported map[string]bool, at time.Time) {
	ds := e.devices[dev.UUID]
	if ds == nil {
		ds = &deviceState{obs: map[string]*observation{}, firstSeen: at}
		e.devices[dev.UUID] = ds
	}
	ex := source.GPUExtras{Sources: []string{sourceID}, ThrottleReasons: []string{}, Processes: []source.GPUProcess{}}
	if extras != nil {
		ex = cloneExtras(*extras)
	}
	dev.Samples = nil
	ds.obs[sourceID] = &observation{dev: dev, extras: ex, at: at, exported: exported}
	ds.lastSeen = at
}

// mergeDeviceLocked rebuilds the merged view of a device from its per-source observations. Gauges come from the
// freshest observation; attribution labels and fault counters are filled in from whichever source has them.
func (e *Engine) mergeDeviceLocked(ds *deviceState, known map[string]bool) {
	if len(ds.obs) == 0 {
		return
	}
	ids := make([]string, 0, len(ds.obs))
	for id := range ds.obs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ds.obs[ids[i]], ds.obs[ids[j]]
		if !a.dev.LastSampleAt.Equal(b.dev.LastSampleAt) {
			return a.dev.LastSampleAt.After(b.dev.LastSampleAt)
		}
		return sourcePriority[ids[i]] < sourcePriority[ids[j]]
	})
	base := ds.obs[ids[0]]
	dev := base.dev
	ex := cloneExtras(base.extras)
	ex.Sources = []string{ids[0]}
	m := e.base.MetricMappings

	for _, id := range ids[1:] {
		o := ds.obs[id]
		ex.Sources = append(ex.Sources, id)
		if dev.AllocatedPod == "" && o.dev.AllocatedPod != "" {
			dev.AllocatedPod, dev.AllocatedNamespace, dev.AllocatedContainer = o.dev.AllocatedPod, o.dev.AllocatedNamespace, o.dev.AllocatedContainer
			dev.MappingStatus = domain.MappingMapped
		}
		if dev.ModelName == "" {
			dev.ModelName = o.dev.ModelName
		}
		if dev.PCIBusID == "" {
			dev.PCIBusID = o.dev.PCIBusID
		}
		if dev.DriverVersion == "" {
			dev.DriverVersion = o.dev.DriverVersion
		}
		if dev.PowerLimitWatts <= 0 {
			dev.PowerLimitWatts = o.dev.PowerLimitWatts
		}
		if dev.TotalMemoryMiB <= 0 {
			dev.TotalMemoryMiB = o.dev.TotalMemoryMiB
		}
		if len(known) > 0 && !known[dev.NodeName] && known[o.dev.NodeName] {
			dev.NodeName = o.dev.NodeName
		}
		if !base.exported[m.XIDErrorsMetric] && o.exported[m.XIDErrorsMetric] {
			dev.Latest.XIDErrorCode = o.dev.Latest.XIDErrorCode
		}
		if !base.exported[m.PCIeReplayMetric] && o.exported[m.PCIeReplayMetric] {
			dev.Latest.PCIeReplayCounter = o.dev.Latest.PCIeReplayCounter
		}
		if !base.exported[m.RowRemapFailureMetric] && o.exported[m.RowRemapFailureMetric] {
			dev.Latest.RowRemapFailure = o.dev.Latest.RowRemapFailure
		}
		if !base.exported[m.UncorrectableRemapMetric] && o.exported[m.UncorrectableRemapMetric] {
			dev.Latest.UncorrectableRemappedRows = o.dev.Latest.UncorrectableRemappedRows
		}
		if !base.exported[m.MemoryTempMetric] && o.exported[m.MemoryTempMetric] {
			dev.Latest.MemoryTempCelsius = o.dev.Latest.MemoryTempCelsius
		}
		if dev.Latest.ECCDBEVolTotal == nil {
			dev.Latest.ECCDBEVolTotal = o.dev.Latest.ECCDBEVolTotal
		}
		if dev.Latest.ECCSBEVolTotal == nil {
			dev.Latest.ECCSBEVolTotal = o.dev.Latest.ECCSBEVolTotal
		}
		if dev.Latest.NVLinkCRCFlitErrors == nil {
			dev.Latest.NVLinkCRCFlitErrors = o.dev.Latest.NVLinkCRCFlitErrors
		}
		if dev.Latest.NVLinkReplayErrors == nil {
			dev.Latest.NVLinkReplayErrors = o.dev.Latest.NVLinkReplayErrors
		}
		if dev.Latest.ThermalViolationNs == nil {
			dev.Latest.ThermalViolationNs = o.dev.Latest.ThermalViolationNs
		}
		if dev.Latest.PowerViolationNs == nil {
			dev.Latest.PowerViolationNs = o.dev.Latest.PowerViolationNs
		}
		if dev.Latest.HealthStatus == nil {
			dev.Latest.HealthStatus = o.dev.Latest.HealthStatus
		}
		if dev.Latest.ClockThrottleReason == "" {
			dev.Latest.ClockThrottleReason = o.dev.Latest.ClockThrottleReason
		}
		if ex.FanSpeedPercent == nil && o.extras.FanSpeedPercent != nil {
			f := *o.extras.FanSpeedPercent
			ex.FanSpeedPercent = &f
		}
		if ex.PState == "" {
			ex.PState = o.extras.PState
		}
		if ex.PCIe == nil && o.extras.PCIe != nil {
			p := *o.extras.PCIe
			ex.PCIe = &p
		}
		if ex.MIGProfile == "" {
			ex.MIGProfile = o.extras.MIGProfile
		}
		for _, r := range o.extras.ThrottleReasons {
			ex.ThrottleReasons = appendUniqueStr(ex.ThrottleReasons, r)
		}
		if len(ex.Processes) == 0 && len(o.extras.Processes) > 0 {
			ex.Processes = append([]source.GPUProcess{}, o.extras.Processes...)
		}
		ex.Emulated = ex.Emulated || o.extras.Emulated
	}
	ds.dev = dev
	ds.extras = ex
}

func appendUniqueStr(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// primaryProcess picks the compute process that holds the most GPU memory (lowest PID breaks ties).
func primaryProcess(procs []source.GPUProcess) (source.GPUProcess, bool) {
	if len(procs) == 0 {
		return source.GPUProcess{}, false
	}
	best := procs[0]
	mem := func(p source.GPUProcess) float64 {
		if p.UsedMemoryMiB == nil {
			return -1
		}
		return *p.UsedMemoryMiB
	}
	for _, p := range procs[1:] {
		if mem(p) > mem(best) || (mem(p) == mem(best) && p.PID < best.PID) {
			best = p
		}
	}
	return best, true
}

func processBaseName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	if name == "" {
		name = "process"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// integrateLocked folds every finished collection into the session state and re-evaluates the rules.
func (e *Engine) integrateLocked(tickStart time.Time) {
	now := time.Now().UTC()
	tickTime := now.Truncate(time.Second)
	outs := e.pending
	e.pending = nil

	// 1. Kubernetes inventory first: node names decide how telemetry labels are interpreted.
	for _, out := range outs {
		if out.id != SourceKubernetes || out.k8s == nil || out.err != nil {
			continue
		}
		for _, a := range out.k8s.Access {
			switch {
			case a.Resource == "nodes" && a.OK:
				e.k8sNodes, e.k8sNodesOK = out.k8s.Nodes, true
			case a.Resource == "pods" && a.OK:
				e.k8sPods, e.k8sPodsOK = out.k8s.Pods, true
			case a.Resource == "events" && a.OK:
				e.k8sEvents = out.k8s.Events
			}
		}
	}
	known := e.knownNodeNamesLocked()
	mapping := source.LiveMetricMapping(e.base.MetricMappings)

	// 2. Device observations from every telemetry source that reported in this cycle.
	for _, out := range outs {
		if out.err != nil {
			continue
		}
		switch out.id {
		case SourceNvidiaSMI:
			if out.smi == nil {
				continue
			}
			for _, g := range out.smi.GPUs {
				e.observeLocked(SourceNvidiaSMI, g, out.smi.Extras[g.UUID], out.smi.Exported, out.at)
			}
		case SourceAMDSMI:
			if out.amd == nil {
				continue
			}
			for _, g := range out.amd.GPUs {
				e.observeLocked(SourceAMDSMI, g, out.amd.Extras[g.UUID], out.amd.Exported, out.at)
			}
		case SourceDCGM:
			if out.dcgm == nil {
				continue
			}
			norm := source.NormalizeSeriesLive(out.dcgm.Series, source.LiveNormalizeOptions{
				Mapping:             mapping,
				MaxStalenessSeconds: e.thresholds.MaxStalenessSeconds,
				Now:                 out.at,
				KnownNodeNames:      known,
				DefaultNodeName:     e.hostname,
				SourceTag:           SourceDCGM,
			})
			if out.dcgm.EmulatorHeader != "" {
				norm.Emulated = true
				for _, ex := range norm.Extras {
					ex.Emulated = true
				}
			}
			st := e.src[SourceDCGM]
			st.dcgmNorm = &norm
			e.describeDCGMLocked(st)
			for _, g := range norm.GPUs {
				e.observeLocked(SourceDCGM, g, norm.Extras[g.UUID], norm.Exported, out.at)
			}
		case SourcePrometheus:
			if out.prom == nil {
				continue
			}
			n := out.prom.Normalized
			for _, g := range n.GPUs {
				e.observeLocked(SourcePrometheus, g, n.Extras[g.UUID], n.Exported, out.at)
			}
			if out.backfillOK {
				e.applyBackfillLocked(out.backfill)
			}
		}
	}

	// 3. Drop observations a healthy source no longer reports, then merge and sample each device.
	for uuid, ds := range e.devices {
		for id, o := range ds.obs {
			st := e.src[id]
			if st == nil || !st.status.Enabled {
				delete(ds.obs, id)
				continue
			}
			if st.status.LastSuccessAt != nil && st.status.LastSuccessAt.After(o.at.Add(time.Second)) {
				delete(ds.obs, id)
			}
		}
		e.mergeDeviceLocked(ds, known)
		d := &ds.dev
		d.StalenessSeconds = now.Sub(d.LastSampleAt).Seconds()
		if d.StalenessSeconds < 0 {
			d.StalenessSeconds = 0
		}
		d.IsStale = d.StalenessSeconds > e.thresholds.MaxStalenessSeconds
		if d.LastSampleAt.After(ds.lastSample) {
			pt := d.Latest
			pt.Timestamp = tickTime
			if n := len(ds.samples); n > 0 && ds.samples[n-1].Timestamp.Equal(tickTime) {
				ds.samples[n-1] = pt
			} else {
				ds.samples = append(ds.samples, pt)
			}
			ds.lastSample = d.LastSampleAt
		}
		if len(ds.obs) == 0 && now.Sub(ds.lastSeen) > deviceForgetAfter {
			e.addActivityLocked(now, "warning", "gpu", fmt.Sprintf("GPU %s removed from the live inventory", shortUUID(uuid)),
				fmt.Sprintf("No source has reported this GPU for %.0f minutes.", deviceForgetAfter.Minutes()))
			delete(e.devices, uuid)
		}
	}
	if (e.tick+1)%thinEveryTicks == 0 {
		for _, ds := range e.devices {
			ds.samples = thinSamples(ds.samples, now)
		}
	}

	e.tick++
	e.assembleAndEvaluateLocked(now)
	e.lastTickAt = now
	e.lastTickDur = time.Since(tickStart)
}

func shortUUID(uuid string) string {
	if len(uuid) > 16 {
		return uuid[:16] + "…"
	}
	return uuid
}

// thinSamples keeps full resolution for the recent past and one sample per minute beyond that, and never drops a
// sample that carries a hardware fault code.
func thinSamples(samples []domain.GPUMetricPoint, now time.Time) []domain.GPUMetricPoint {
	if len(samples) == 0 {
		return samples
	}
	rawFrom := now.Add(-rawSampleWindow)
	oldest := now.Add(-sampleRetention)
	out := make([]domain.GPUMetricPoint, 0, len(samples))
	var lastBucket time.Time
	for i, s := range samples {
		if s.Timestamp.Before(oldest) {
			continue
		}
		if !s.Timestamp.Before(rawFrom) {
			out = append(out, samples[i:]...)
			break
		}
		bucket := s.Timestamp.Truncate(time.Minute)
		if s.XIDErrorCode != 0 || !bucket.Equal(lastBucket) {
			out = append(out, s)
			lastBucket = bucket
		}
	}
	return out
}

// applyBackfillLocked merges Prometheus range-query history in front of the samples collected by this session.
func (e *Engine) applyBackfillLocked(history map[string][]domain.GPUMetricPoint) {
	devices, oldest := 0, time.Time{}
	for uuid, points := range history {
		ds := e.devices[uuid]
		if ds == nil || len(points) == 0 {
			continue
		}
		cutoff := time.Now().UTC()
		if len(ds.samples) > 0 {
			cutoff = ds.samples[0].Timestamp
		}
		var older []domain.GPUMetricPoint
		for _, p := range points {
			if p.Timestamp.Before(cutoff) {
				older = append(older, p)
			}
		}
		if len(older) == 0 {
			continue
		}
		ds.samples = append(older, ds.samples...)
		devices++
		if oldest.IsZero() || older[0].Timestamp.Before(oldest) {
			oldest = older[0].Timestamp
		}
	}
	e.backfill.devices = devices
	e.backfill.pendingHistory = devices > 0
	if devices > 0 {
		e.backfill.minutes = time.Since(oldest).Minutes()
		e.addActivityLocked(time.Now().UTC(), "info", "source", "History backfilled from Prometheus",
			fmt.Sprintf("Loaded %.0f minutes of utilization history for %d GPU(s) with one read-only range query.", e.backfill.minutes, devices))
	}
}

// resampleWindow averages a device's samples into equal time buckets inside [from, now], so that a mean over
// the window weights every part of it equally regardless of how densely it was sampled.
func resampleWindow(samples []domain.GPUMetricPoint, from time.Time, bucket time.Duration) []domain.GPUMetricPoint {
	var out []domain.GPUMetricPoint
	var cur domain.GPUMetricPoint
	var curBucket time.Time
	n := 0
	flush := func() {
		if n == 0 {
			return
		}
		f := float64(n)
		cur.GPUUtilPercent /= f
		cur.MemCopyUtilPercent /= f
		cur.FBUsedMiB /= f
		cur.FBFreeMiB /= f
		cur.GPUTempCelsius /= f
		cur.MemoryTempCelsius /= f
		cur.PowerUsageWatts /= f
		cur.Timestamp = curBucket
		out = append(out, cur)
		n = 0
	}
	for _, s := range samples {
		if s.Timestamp.Before(from) {
			continue
		}
		b := s.Timestamp.Truncate(bucket)
		if n == 0 || !b.Equal(curBucket) {
			flush()
			curBucket = b
			cur = domain.GPUMetricPoint{}
		}
		cur.GPUUtilPercent += s.GPUUtilPercent
		cur.MemCopyUtilPercent += s.MemCopyUtilPercent
		cur.FBUsedMiB += s.FBUsedMiB
		cur.FBFreeMiB += s.FBFreeMiB
		cur.GPUTempCelsius += s.GPUTempCelsius
		cur.MemoryTempCelsius += s.MemoryTempCelsius
		cur.PowerUsageWatts += s.PowerUsageWatts
		if s.XIDErrorCode != 0 {
			cur.XIDErrorCode = s.XIDErrorCode
		}
		if s.PCIeReplayCounter > cur.PCIeReplayCounter {
			cur.PCIeReplayCounter = s.PCIeReplayCounter
		}
		if s.RowRemapFailure > cur.RowRemapFailure {
			cur.RowRemapFailure = s.RowRemapFailure
		}
		if s.UncorrectableRemappedRows > cur.UncorrectableRemappedRows {
			cur.UncorrectableRemappedRows = s.UncorrectableRemappedRows
		}
		n++
	}
	flush()
	return out
}

// anchorWindow stretches a resampled series to the edges of the observation window once the raw series demonstrably
// covered it. Bucket timestamps are truncated bucket starts, so without this the series would always look slightly
// shorter than the window and the capacity calculator (which measures the span of the samples it is given) would
// disagree with the rule engine about whether the window was observed.
func anchorWindow(raw, out []domain.GPUMetricPoint, from, now time.Time, bucket time.Duration) []domain.GPUMetricPoint {
	if len(out) < 2 || len(raw) == 0 {
		return out
	}
	if raw[0].Timestamp.After(from.Add(bucket)) {
		return out // the raw series started inside the window: it did not cover all of it
	}
	out[0].Timestamp = from
	out[len(out)-1].Timestamp = now
	return out
}

func lowUtilBucket(th config.RuleThresholds, interval time.Duration) time.Duration {
	bucket := time.Duration(th.LowUtilWindowMinutes * float64(time.Minute) / 60)
	if bucket > time.Minute {
		bucket = time.Minute
	}
	if bucket < interval {
		bucket = interval
	}
	if bucket < time.Second {
		bucket = time.Second
	}
	return bucket.Truncate(time.Second)
}

// assembleAndEvaluateLocked builds the cluster snapshot from the merged state and runs rules and capacity math.
func (e *Engine) assembleAndEvaluateLocked(now time.Time) {
	known := e.knownNodeNamesLocked()
	facts := mergeFacts{}

	uuids := make([]string, 0, len(e.devices))
	for uuid := range e.devices {
		uuids = append(uuids, uuid)
	}
	sort.Slice(uuids, func(i, j int) bool {
		a, b := e.devices[uuids[i]].dev, e.devices[uuids[j]].dev
		if a.NodeName != b.NodeName {
			return a.NodeName < b.NodeName
		}
		if a.DeviceIndex != b.DeviceIndex {
			return a.DeviceIndex < b.DeviceIndex
		}
		return a.UUID < b.UUID
	})

	// --- Nodes -------------------------------------------------------------------------------------
	nodes := make([]domain.NodeState, len(e.k8sNodes))
	copy(nodes, e.k8sNodes)
	byNode := map[string][]string{}
	for _, uuid := range uuids {
		name := e.devices[uuid].dev.NodeName
		byNode[name] = append(byNode[name], uuid)
	}
	inferred := map[string]bool{}
	nodeNames := make([]string, 0, len(byNode))
	for name := range byNode {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	for _, name := range nodeNames {
		if known[name] {
			continue
		}
		list := byNode[name]
		model := ""
		for _, uuid := range list {
			if m := e.devices[uuid].dev.ModelName; m != "" {
				model = m
				break
			}
		}
		nodes = append(nodes, domain.NodeState{
			Name: name, Ready: true, Schedulable: true, GPUModel: model,
			GPUCapacity: len(list), GPUAllocatable: len(list),
			Labels: map[string]string{inferredNodeLabel: "telemetry"},
		})
		inferred[name] = true
		facts.inferredNodes = append(facts.inferredNodes, name)
		if e.k8sNodesOK {
			// A node name from cluster telemetry that Kubernetes does not know is a labelling problem; the
			// local workstation's own GPUs (nvidia-smi only) are simply outside the cluster.
			for _, uuid := range list {
				for _, src := range e.devices[uuid].extras.Sources {
					if !isLocalDriverSource(src) {
						facts.unmatchedNodes = appendUniqueStr(facts.unmatchedNodes, name)
					}
				}
			}
		}
	}
	for i := range nodes {
		if nodes[i].GPUModel == "" || nodes[i].GPUModel == "NVIDIA Datacenter GPU" {
			for _, uuid := range byNode[nodes[i].Name] {
				if m := e.devices[uuid].dev.ModelName; m != "" {
					nodes[i].GPUModel = m
					break
				}
			}
		}
		if !inferred[nodes[i].Name] {
			facts.k8sAllocatedGPUs += nodes[i].GPUAllocated
			facts.k8sGPUCapacity += nodes[i].GPUCapacity
		}
	}

	// --- Pods + attribution ------------------------------------------------------------------------
	pods := make([]domain.WorkloadPod, len(e.k8sPods))
	for i, p := range e.k8sPods {
		p.AssignedGPUUUIDs = nil
		pods[i] = p
	}
	podIndex := map[string]int{}
	for i, p := range pods {
		podIndex[p.Namespace+"/"+p.Name] = i
	}
	observedSince := map[string]time.Time{}
	noteObserved := func(key string, t time.Time) {
		if cur, ok := observedSince[key]; !ok || t.After(cur) {
			observedSince[key] = t
		}
	}

	addHostWorkload := func(ds *deviceState, uuid string) {
		d := &ds.dev
		name := fmt.Sprintf("%s[%d]", processBaseName(ds.procName), ds.procPID)
		key := HostProcessNamespace + "/" + name
		d.AllocatedNamespace, d.AllocatedPod, d.AllocatedContainer = HostProcessNamespace, name, ""
		d.MappingStatus = domain.MappingMapped
		ds.hostProc = true
		if i, ok := podIndex[key]; ok {
			pods[i].AssignedGPUUUIDs = append(pods[i].AssignedGPUUUIDs, uuid)
			pods[i].RequestedGPUs = len(pods[i].AssignedGPUUUIDs)
			if ds.procSince.Before(*pods[i].StartedAt) {
				since := ds.procSince
				pods[i].StartedAt = &since
				pods[i].CreatedAt = since
			}
			return
		}
		since := ds.procSince
		pods = append(pods, domain.WorkloadPod{
			Name: name, Namespace: HostProcessNamespace, UID: fmt.Sprintf("host-process:%s:%d", d.NodeName, ds.procPID),
			OwnerKind: "HostProcess", OwnerName: ds.procName, Phase: "Running", NodeName: d.NodeName,
			RequestedGPUs: 1, AssignedGPUUUIDs: []string{uuid}, CreatedAt: since, StartedAt: &since,
			StatusDetail: noteHostProcess,
		})
		podIndex[key] = len(pods) - 1
		facts.hostWorkloads++
	}

	for _, uuid := range uuids {
		ds := e.devices[uuid]
		d := &ds.dev
		if len(ds.obs) == 0 && ds.hostProc {
			// No source reports this GPU right now: keep its last known host-process attribution as it was.
			addHostWorkload(ds, uuid)
			continue
		}
		ds.hostProc = false
		if d.AllocatedPod != "" {
			key := d.AllocatedNamespace + "/" + d.AllocatedPod
			if i, ok := podIndex[key]; ok {
				pods[i].AssignedGPUUUIDs = append(pods[i].AssignedGPUUUIDs, uuid)
				if strings.HasPrefix(pods[i].UID, "inferred:") {
					pods[i].RequestedGPUs = len(pods[i].AssignedGPUUUIDs)
				}
			} else if !e.k8sPodsOK {
				started := ds.firstSeen
				pods = append(pods, domain.WorkloadPod{
					Name: d.AllocatedPod, Namespace: d.AllocatedNamespace, UID: "inferred:" + key,
					OwnerKind: "Pod", OwnerName: d.AllocatedPod, Phase: "Running", NodeName: d.NodeName,
					RequestedGPUs: 1, AssignedGPUUUIDs: []string{uuid}, CreatedAt: started, StartedAt: &started,
					StatusDetail: noteInferredFromDCG,
				})
				podIndex[key] = len(pods) - 1
				facts.inferredPods++
			} else {
				facts.unknownPodGPUs++
			}
			continue
		}
		// Standalone host: attribute the GPU to the compute process that holds most of its memory.
		if !inferred[d.NodeName] {
			continue
		}
		proc, ok := primaryProcess(ds.extras.Processes)
		if !ok {
			ds.procPID = 0
			continue
		}
		if ds.procPID != proc.PID {
			ds.procPID = proc.PID
			ds.procSince = now
		}
		ds.procName = proc.Name
		addHostWorkload(ds, uuid)
	}

	// Allocation on inferred (telemetry-only) nodes = GPUs that are attributed to a workload.
	for i := range nodes {
		if !inferred[nodes[i].Name] {
			continue
		}
		used := 0
		for _, uuid := range byNode[nodes[i].Name] {
			if e.devices[uuid].dev.AllocatedPod != "" {
				used++
			}
		}
		nodes[i].GPUAllocated = used
	}

	// Kubernetes says N GPUs are allocated on a node but fewer series carry pod labels: flag the unlabelled
	// devices only when it is certain which ones they are.
	for _, n := range nodes {
		if inferred[n.Name] || n.GPUAllocated <= 0 {
			continue
		}
		list := byNode[n.Name]
		if len(list) == 0 {
			continue
		}
		mapped := 0
		var unlabelled []string
		for _, uuid := range list {
			ds := e.devices[uuid]
			if ds.dev.MappingStatus == domain.MappingMapped {
				mapped++
			} else if !ds.dev.IsStale {
				unlabelled = append(unlabelled, uuid)
			}
		}
		missing := n.GPUAllocated - mapped
		if missing <= 0 {
			continue
		}
		if len(unlabelled) == missing {
			for _, uuid := range unlabelled {
				e.devices[uuid].dev.MappingStatus = domain.MappingMissingPodLabels
			}
		} else if len(unlabelled) > missing {
			facts.attributionGaps = append(facts.attributionGaps,
				fmt.Sprintf("%s: Kubernetes reports %d allocated GPU(s), %d series carry pod labels, %d unlabelled", n.Name, n.GPUAllocated, mapped, len(unlabelled)))
		}
	}
	// --- Devices for the snapshot ------------------------------------------------------------------
	gpus := make([]domain.GPUDevice, 0, len(uuids))
	for _, uuid := range uuids {
		ds := e.devices[uuid]
		d := ds.dev
		d.Samples = ds.samples
		gpus = append(gpus, d)
		facts.totalGPUs++
		if !d.IsStale {
			facts.freshGPUs++
		}
		if d.MappingStatus == domain.MappingMapped {
			facts.mappedGPUs++
		}
		if len(ds.extras.Sources) == 1 && ds.extras.Sources[0] == SourceNvidiaSMI {
			facts.smiOnlyGPUs++
		}
		if len(ds.extras.Sources) == 1 && ds.extras.Sources[0] == SourceAMDSMI {
			facts.amdGPUs++
		}
		if ds.extras.Emulated {
			facts.emulatedGPUs++
		}
		if d.AllocatedPod != "" {
			noteObserved(d.AllocatedNamespace+"/"+d.AllocatedPod, firstSampleTime(ds))
		}
	}

	// --- Sources + exported metrics ----------------------------------------------------------------
	e.exported = map[string]bool{}
	if st := e.src[SourceNvidiaSMI]; st != nil && st.smi != nil && st.status.State != StateDisabled {
		for k, v := range st.smi.Exported {
			e.exported[k] = e.exported[k] || v
		}
	}
	if st := e.src[SourceAMDSMI]; st != nil && st.amd != nil && st.status.State != StateDisabled {
		for k, v := range st.amd.Exported {
			e.exported[k] = e.exported[k] || v
		}
	}
	if st := e.src[SourceDCGM]; st != nil && st.dcgmNorm != nil {
		for k, v := range st.dcgmNorm.Exported {
			e.exported[k] = e.exported[k] || v
		}
	}
	if st := e.src[SourcePrometheus]; st != nil && st.prom != nil {
		for k, v := range st.prom.Normalized.Exported {
			e.exported[k] = e.exported[k] || v
		}
	}
	delete(e.exported, source.LabEmulatorMarkerMetric)
	sources := e.sourceHealthLocked(now)

	emulated := false
	for _, st := range e.src {
		if st.status.Emulated && (st.status.State == StateConnected || st.status.State == StateDegraded) {
			emulated = true
		}
	}
	dataMode := "live_telemetry"
	if emulated {
		dataMode = "live_emulated"
	}
	clusterName := e.cfg.ClusterName
	if clusterName == "" {
		clusterName = "live@" + e.hostname
	}

	snap := &domain.ClusterSnapshot{
		ScenarioID:          "live",
		ScenarioName:        "Live Telemetry",
		ScenarioDescription: e.describeSessionLocked(facts, emulated),
		ClusterName:         clusterName,
		DataMode:            dataMode,
		CollectedAt:         now,
		Sources:             sources,
		Nodes:               nodes,
		GPUs:                gpus,
		Pods:                pods,
		Events:              e.k8sEvents,
		ExportedMetrics:     e.exported,
	}

	// --- Evaluation --------------------------------------------------------------------------------
	// The low-utilization rule and the capacity report look at an evenly resampled window, and a workload only
	// counts as "running for the whole window" once GPU Lens itself has watched it that long.
	windowFrom := now.Add(-time.Duration(e.thresholds.LowUtilWindowMinutes * float64(time.Minute)))
	bucket := lowUtilBucket(e.thresholds, e.effectiveInterval())
	windowed := *snap
	windowed.GPUs = make([]domain.GPUDevice, len(gpus))
	for i, g := range gpus {
		g.Samples = anchorWindow(g.Samples, resampleWindow(g.Samples, windowFrom, bucket), windowFrom, now, bucket)
		windowed.GPUs[i] = g
	}
	windowed.Pods = make([]domain.WorkloadPod, len(pods))
	for i, p := range pods {
		if p.StartedAt != nil {
			if seen, ok := observedSince[p.Namespace+"/"+p.Name]; ok && seen.After(*p.StartedAt) {
				clamped := seen
				p.StartedAt = &clamped
			} else if !ok && len(p.AssignedGPUUUIDs) == 0 {
				// No attributed telemetry at all: nothing was observed for this pod.
				clamped := now
				p.StartedAt = &clamped
			}
		}
		windowed.Pods[i] = p
	}

	var incidents []domain.Incident
	for _, inc := range e.rules.Evaluate(snap, e.thresholds) {
		if inc.RuleID != lowUtilRuleID {
			incidents = append(incidents, inc)
		}
	}
	for _, inc := range e.rules.Evaluate(&windowed, e.thresholds) {
		if inc.RuleID == lowUtilRuleID {
			incidents = append(incidents, inc)
		}
	}
	sortIncidents(incidents)
	e.annotateProvenanceLocked(incidents)
	incidents = e.trackIncidentsLocked(incidents, now)
	// Resolutions are only announced once the session has settled, so a restart does not report every incident
	// as resolved before its sources have been read again.
	e.alerts.Sync(incidents, alert.Meta{Cluster: snap.ClusterName, DataMode: snap.DataMode, Version: Version}, e.tick >= 3)

	capReport := capacity.BuildReport(&windowed, e.thresholds)
	for i := range capReport.Nodes {
		if inferred[capReport.Nodes[i].NodeName] && strings.HasPrefix(capReport.Nodes[i].StatusNote, "Healthy") {
			capReport.Nodes[i].StatusNote = "Telemetry-only host (no Kubernetes scheduler state)"
		}
	}

	e.merge = facts
	e.snapshot = snap
	e.viewNodes = nodes
	e.viewPods = pods
	e.overview = buildOverview(snap, incidents, capReport, facts, e.k8sNodesOK)
	e.buildViewsLocked(uuids, incidents)
	e.recordHistoryLocked(now, incidents)
	if e.backfill.pendingHistory {
		e.backfill.pendingHistory = false
		e.prependBackfillHistoryLocked()
	}
	e.diffActivityLocked(now, incidents)
}

func firstSampleTime(ds *deviceState) time.Time {
	if len(ds.samples) > 0 {
		return ds.samples[0].Timestamp
	}
	return ds.firstSeen
}

func severityRank(s domain.Severity) int {
	switch s {
	case domain.SeverityCritical:
		return 3
	case domain.SeverityWarning:
		return 2
	}
	return 1
}

func sortIncidents(list []domain.Incident) {
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := severityRank(list[i].Severity), severityRank(list[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if list[i].RuleID != list[j].RuleID {
			return list[i].RuleID < list[j].RuleID
		}
		return list[i].ID < list[j].ID
	})
}

func (e *Engine) describeSessionLocked(f mergeFacts, emulated bool) string {
	var parts []string
	for _, id := range []string{SourceNvidiaSMI, SourceAMDSMI, SourceDCGM, SourcePrometheus, SourceKubernetes} {
		st := e.src[id]
		if st != nil && (st.status.State == StateConnected || st.status.State == StateDegraded) {
			parts = append(parts, st.status.Name)
		}
	}
	if len(parts) == 0 {
		return "No GPU telemetry source is connected. Nothing is being monitored."
	}
	text := fmt.Sprintf("Live read-only collection from %s — %d GPU(s) observed.", strings.Join(parts, ", "), f.totalGPUs)
	if emulated {
		text += " At least one source is the GPU Lens Live Lab emulator: its values are synthetic."
	}
	return text
}

// sourceHealthLocked lists the telemetry sources the rules should hold to account: those the operator configured
// and those that have answered at least once this session. A default address that never answered is not a source.
func (e *Engine) sourceHealthLocked(now time.Time) []domain.SourceHealth {
	var out []domain.SourceHealth
	reqNames := source.RequiredDCGMMetrics(e.base.MetricMappings)
	optNames := source.OptionalDCGMMetrics(e.base.MetricMappings)
	for _, id := range []string{SourceNvidiaSMI, SourceAMDSMI, SourceDCGM, SourcePrometheus, SourceKubernetes} {
		st := e.src[id]
		if st == nil || !st.status.Enabled {
			continue
		}
		if !st.everConnected && !(st.status.Explicit && !isLocalDriverSource(id)) && st.status.State != StateError {
			continue
		}
		if st.status.State == StatePending {
			continue
		}
		available := st.status.State == StateConnected || st.status.State == StateDegraded
		h := domain.SourceHealth{
			ID: id, Name: st.status.Name, Kind: st.status.Kind, Mode: "live", Endpoint: st.status.Endpoint,
			Available: available, ReadOnlyVerified: e.guard.OK(), LastError: st.status.LastError, Notes: st.status.Detail,
		}
		if st.status.Emulated {
			h.Mode = "live (emulated endpoint)"
		}
		ref := e.startedAt
		if st.status.LastSuccessAt != nil {
			h.LastSuccessAt = *st.status.LastSuccessAt
			ref = *st.status.LastSuccessAt
		}
		h.StalenessSeconds = now.Sub(ref).Seconds()
		if h.StalenessSeconds < 0 {
			h.StalenessSeconds = 0
		}
		h.IsStale = h.StalenessSeconds > e.thresholds.MaxStalenessSeconds
		var exported map[string]bool
		switch id {
		case SourceNvidiaSMI:
			if st.smi != nil {
				exported = st.smi.Exported
			}
		case SourceAMDSMI:
			if st.amd != nil {
				exported = st.amd.Exported
			}
		case SourceDCGM:
			if st.dcgmNorm != nil {
				exported = st.dcgmNorm.Exported
			}
		case SourcePrometheus:
			if st.prom != nil {
				exported = st.prom.Normalized.Exported
			}
		}
		if exported != nil {
			h.ExportedRequiredCount, h.MissingRequired = source.MissingMetrics(reqNames, exported)
			h.ExportedOptionalCount, h.MissingOptional = source.MissingMetrics(optNames, exported)
		}
		out = append(out, h)
	}
	return out
}

// buildOverview mirrors the fixture overview payload, with one extra verdict: when nothing is connected the fleet
// is reported as unmonitored, never as healthy.
func buildOverview(snap *domain.ClusterSnapshot, incidents []domain.Incident, capReport domain.CapacityReport, f mergeFacts, k8sOK bool) *domain.OverviewResponse {
	counts := map[string]int{"critical": 0, "warning": 0, "info": 0, "inconclusive": 0, "total": len(incidents)}
	blind := false
	for _, inc := range incidents {
		counts[string(inc.Severity)]++
		if inc.EvalStatus == domain.EvalStatusInconclusive {
			counts["inconclusive"]++
		}
		if inc.RuleID == "GPU-FRESH-005" {
			blind = true
		}
	}
	status := "healthy"
	headline := "All monitored GPUs, nodes, and telemetry streams are operating within healthy envelopes."
	switch {
	case blind:
		status = "telemetry_blind_spot"
		headline = "Monitoring blind spot detected: stale, unavailable, or uncorrelated GPU telemetry. Missing data is never treated as healthy."
	case f.totalGPUs == 0 && !k8sOK:
		status = "no_telemetry"
		headline = "No GPU telemetry source is connected, so nothing is being monitored. This is not a healthy verdict — connect a source under Connections."
	case f.totalGPUs == 0 && f.k8sGPUCapacity == 0:
		status = "no_telemetry"
		headline = "Kubernetes is connected but advertises no nvidia.com/gpu capacity, and no GPU telemetry source is connected. Nothing is being monitored."
	case f.totalGPUs == 0:
		status = "telemetry_blind_spot"
		headline = "Kubernetes inventory is live, but no GPU hardware telemetry is connected: hardware-health, thermal and utilization rules are blind."
	case counts["critical"] > 0:
		status = "critical"
		headline = "Critical GPU hardware, scheduling, or workload failure findings require operator investigation."
	case counts["warning"] > 0:
		status = "warning"
		headline = "Active GPU scheduling, thermal, or utilization warnings detected."
	}
	if incidents == nil {
		incidents = []domain.Incident{}
	}
	return &domain.OverviewResponse{
		ClusterName:          snap.ClusterName,
		ScenarioID:           snap.ScenarioID,
		ScenarioName:         snap.ScenarioName,
		ScenarioDescription:  snap.ScenarioDescription,
		DataMode:             snap.DataMode,
		CollectedAt:          snap.CollectedAt,
		FleetHealthStatus:    status,
		FleetHealthHeadline:  headline,
		ActiveIncidentCounts: counts,
		CapacitySummary:      capReport,
		ActiveIncidents:      incidents,
		Sources:              snap.Sources,
	}
}

// annotateProvenanceLocked corrects evidence provenance for GPUs that were read through nvidia-smi: the rules
// name signals by their canonical DCGM field, but the reading itself came from the local NVIDIA driver.
func (e *Engine) annotateProvenanceLocked(incidents []domain.Incident) {
	localOnly := map[string]string{}
	for uuid, ds := range e.devices {
		if len(ds.extras.Sources) == 1 && isLocalDriverSource(ds.extras.Sources[0]) {
			localOnly[uuid] = ds.extras.Sources[0]
		}
	}
	if len(localOnly) == 0 {
		return
	}
	touches := func(ref string) string {
		for uuid, src := range localOnly {
			if strings.Contains(ref, uuid) {
				return src
			}
		}
		return ""
	}
	notes := map[string]string{
		SourceNvidiaSMI: "Telemetry for the affected GPU was read from the local NVIDIA driver via nvidia-smi; DCGM field names denote the equivalent canonical signals.",
		SourceAMDSMI:    "Telemetry for the affected GPU was read from the local AMD driver via amd-smi; DCGM field names denote the equivalent canonical signals.",
	}
	for i := range incidents {
		inc := &incidents[i]
		via := ""
		for _, list := range [][]domain.EvidenceItem{inc.SupportingEvidence, inc.ContradictingEvidence} {
			for j := range list {
				if src := touches(list[j].ObjectRef); list[j].Source == "dcgm_exporter" && src != "" {
					list[j].Source = src
					via = src
				}
			}
		}
		if via == "" {
			for _, r := range inc.AffectedResources {
				if src := touches(r); src != "" {
					via = src
					break
				}
			}
		}
		if via != "" {
			inc.ObservedFacts = append(inc.ObservedFacts, notes[via])
		}
	}
}
