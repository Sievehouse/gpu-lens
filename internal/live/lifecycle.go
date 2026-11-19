// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package live

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/domain"
	"github.com/Sievehouse/gpu-lens/internal/source"
)

const maxActivityPerTick = 24

// diffState is what the previous cycle looked like, used to derive the change log.
type diffState struct {
	initialized bool
	sources     map[string]string
	gpus        map[string]gpuDiff
	nodes       map[string]nodeDiff
	pods        map[string]podDiff
	events      map[string]int
	fleet       string
}

type gpuDiff struct {
	stale    bool
	hot      bool
	xid      int
	throttle string
	node     string
	index    int
}

type nodeDiff struct {
	ready       bool
	schedulable bool
	allocatable int
}

type podDiff struct {
	phase    string
	restarts int
}

// trackIncidentsLocked carries first-seen time and occurrence counts across cycles and records what resolved.
func (e *Engine) trackIncidentsLocked(incidents []domain.Incident, now time.Time) []domain.Incident {
	next := make(map[string]domain.Incident, len(incidents))
	for i := range incidents {
		inc := &incidents[i]
		prev, existed := e.open[inc.Fingerprint]
		if existed {
			if !prev.FirstSeenAt.IsZero() && (inc.FirstSeenAt.IsZero() || prev.FirstSeenAt.Before(inc.FirstSeenAt)) {
				inc.FirstSeenAt = prev.FirstSeenAt
			}
			inc.OccurrenceCount = prev.OccurrenceCount + 1
			if prev.Severity != inc.Severity {
				sev := "warning"
				if inc.Severity == domain.SeverityCritical {
					sev = "critical"
				}
				e.addActivityLocked(now, sev, "incident", fmt.Sprintf("Severity changed: %s → %s", prev.Severity, inc.Severity), inc.Title)
			}
		} else {
			inc.OccurrenceCount = 1
			if inc.FirstSeenAt.IsZero() {
				inc.FirstSeenAt = now
			}
			sev := string(inc.Severity)
			if inc.EvalStatus == domain.EvalStatusInconclusive {
				sev = "inconclusive"
			}
			if since, ok := e.restoredFirstSeen[inc.Fingerprint]; ok {
				// Still firing after a GPU Lens restart: keep its original age instead of reporting a new incident.
				delete(e.restoredFirstSeen, inc.Fingerprint)
				if since.Before(inc.FirstSeenAt) {
					inc.FirstSeenAt = since
				}
				e.addActivityLocked(now, sev, "incident", fmt.Sprintf("Incident still open [%s]", inc.RuleID),
					inc.Title+" — open since before GPU Lens restarted.")
			} else {
				e.addActivityLocked(now, sev, "incident", fmt.Sprintf("Incident opened [%s]", inc.RuleID), inc.Title)
			}
		}
		if inc.LastSeenAt.Before(now) {
			inc.LastSeenAt = now
		}
		next[inc.Fingerprint] = *inc
	}
	var gone []domain.Incident
	for fp, prev := range e.open {
		if _, still := next[fp]; !still {
			gone = append(gone, prev)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].ID < gone[j].ID })
	for _, prev := range gone {
		dur := now.Sub(prev.FirstSeenAt).Seconds()
		if dur < 0 {
			dur = 0
		}
		e.resolved = append(e.resolved, ResolvedIncident{Incident: prev, ResolvedAt: now, DurationSeconds: dur})
		e.addActivityLocked(now, "success", "incident", fmt.Sprintf("Incident resolved [%s]", prev.RuleID),
			fmt.Sprintf("%s — no longer firing after %s.", prev.Title, humanDuration(dur)))
	}
	if len(e.resolved) > maxResolvedIncidents {
		e.resolved = append([]ResolvedIncident(nil), e.resolved[len(e.resolved)-maxResolvedIncidents:]...)
	}
	e.open = next
	if e.tick >= 3 {
		e.restoredFirstSeen = nil
	}
	return incidents
}

func humanDuration(seconds float64) string {
	switch {
	case seconds < 90:
		return fmt.Sprintf("%.0fs", seconds)
	case seconds < 5400:
		return fmt.Sprintf("%.0f min", seconds/60)
	default:
		return fmt.Sprintf("%.1f h", seconds/3600)
	}
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func fbPercent(d domain.GPUDevice) (float64, bool) {
	total := d.TotalMemoryMiB
	if total <= 0 {
		total = d.Latest.FBUsedMiB + d.Latest.FBFreeMiB
	}
	if total <= 0 {
		return 0, false
	}
	return d.Latest.FBUsedMiB / total * 100.0, true
}

// recordHistoryLocked appends one fleet-level point to the rolling session history.
func (e *Engine) recordHistoryLocked(now time.Time, incidents []domain.Incident) {
	pt := HistoryPoint{T: now, G: map[string][]float64{}}
	var sumUtil, sumFB, maxTemp, sumPower float64
	fbCount := 0
	for uuid, ds := range e.devices {
		d := ds.dev
		pt.GPUs++
		if d.IsStale {
			continue
		}
		pt.FreshGPUs++
		sumUtil += d.Latest.GPUUtilPercent
		fb, ok := fbPercent(d)
		if ok {
			sumFB += fb
			fbCount++
		}
		if d.Latest.GPUTempCelsius > maxTemp {
			maxTemp = d.Latest.GPUTempCelsius
		}
		sumPower += d.Latest.PowerUsageWatts
		pt.G[uuid] = []float64{round1(d.Latest.GPUUtilPercent), round1(fb), round1(d.Latest.GPUTempCelsius), round1(d.Latest.PowerUsageWatts)}
	}
	if pt.FreshGPUs > 0 {
		util := round1(sumUtil / float64(pt.FreshGPUs))
		temp := round1(maxTemp)
		power := round1(sumPower)
		pt.Util, pt.TempMax, pt.Power = &util, &temp, &power
		if fbCount > 0 {
			fb := round1(sumFB / float64(fbCount))
			pt.FB = &fb
		}
	}
	for _, inc := range incidents {
		switch inc.Severity {
		case domain.SeverityCritical:
			pt.Critical++
		case domain.SeverityWarning:
			pt.Warning++
		}
	}
	if e.hostVitals != nil {
		if e.hostVitals.CPUPercent != nil {
			v := round1(*e.hostVitals.CPUPercent)
			pt.HostCPU = &v
		}
		if e.hostVitals.MemUsedPercent != nil {
			v := round1(*e.hostVitals.MemUsedPercent)
			pt.HostMem = &v
		}
	}
	e.seq++
	pt.Seq = e.seq
	e.history = append(e.history, pt)
	if limit := e.cfg.HistoryPoints; limit > 0 && len(e.history) > limit {
		e.history = append([]HistoryPoint(nil), e.history[len(e.history)-limit:]...)
	}
}

// prependBackfillHistoryLocked turns backfilled per-GPU samples into fleet points placed before the session's own
// history. Sequence numbers are reassigned, so the history epoch changes and clients reload the series.
func (e *Engine) prependBackfillHistoryLocked() {
	firstLive := time.Now().UTC()
	if len(e.history) > 0 {
		firstLive = e.history[0].T
	}
	type agg struct {
		util, fb, power, temp float64
		n, fbN                int
		g                     map[string][]float64
	}
	byTS := map[int64]*agg{}
	for uuid, ds := range e.devices {
		for _, s := range ds.samples {
			// Leave a one-second gap so the first live point is not duplicated by its own sample.
			if !s.Timestamp.Before(firstLive.Add(-time.Second)) {
				break
			}
			a := byTS[s.Timestamp.Unix()]
			if a == nil {
				a = &agg{g: map[string][]float64{}}
				byTS[s.Timestamp.Unix()] = a
			}
			a.n++
			a.util += s.GPUUtilPercent
			a.power += s.PowerUsageWatts
			if s.GPUTempCelsius > a.temp {
				a.temp = s.GPUTempCelsius
			}
			fb := 0.0
			total := ds.dev.TotalMemoryMiB
			if total <= 0 {
				total = s.FBUsedMiB + s.FBFreeMiB
			}
			if total > 0 {
				fb = s.FBUsedMiB / total * 100.0
				a.fb += fb
				a.fbN++
			}
			a.g[uuid] = []float64{round1(s.GPUUtilPercent), round1(fb), round1(s.GPUTempCelsius), round1(s.PowerUsageWatts)}
		}
	}
	if len(byTS) == 0 {
		return
	}
	keys := make([]int64, 0, len(byTS))
	for k := range byTS {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	older := make([]HistoryPoint, 0, len(keys))
	for _, k := range keys {
		a := byTS[k]
		util, temp, power := round1(a.util/float64(a.n)), round1(a.temp), round1(a.power)
		pt := HistoryPoint{T: time.Unix(k, 0).UTC(), GPUs: a.n, FreshGPUs: a.n, Util: &util, TempMax: &temp, Power: &power, Backfilled: true, G: a.g}
		if a.fbN > 0 {
			fb := round1(a.fb / float64(a.fbN))
			pt.FB = &fb
		}
		older = append(older, pt)
	}
	merged := append(older, e.history...)
	if limit := e.cfg.HistoryPoints; limit > 0 && len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	for i := range merged {
		merged[i].Seq = i + 1
	}
	e.history = merged
	e.seq = len(merged)
	e.historyEpoch++
}

func gpuLabel(d domain.GPUDevice) string {
	return fmt.Sprintf("GPU %d on %s", d.DeviceIndex, d.NodeName)
}

// diffActivityLocked compares this cycle with the previous one and logs what changed.
func (e *Engine) diffActivityLocked(now time.Time, incidents []domain.Incident) {
	cur := diffState{
		initialized: true,
		sources:     map[string]string{},
		gpus:        map[string]gpuDiff{},
		nodes:       map[string]nodeDiff{},
		pods:        map[string]podDiff{},
		events:      map[string]int{},
	}
	if e.overview != nil {
		cur.fleet = e.overview.FleetHealthStatus
	}
	for id, st := range e.src {
		cur.sources[id] = st.status.State
	}
	for uuid, ds := range e.devices {
		d := ds.dev
		cur.gpus[uuid] = gpuDiff{
			stale:    d.IsStale,
			hot:      !d.IsStale && d.Latest.GPUTempCelsius >= e.thresholds.TempWarningCelsius,
			xid:      d.Latest.XIDErrorCode,
			throttle: strings.Join(ds.extras.ThrottleReasons, ", "),
			node:     d.NodeName,
			index:    d.DeviceIndex,
		}
	}
	for _, n := range e.viewNodes {
		cur.nodes[n.Name] = nodeDiff{ready: n.Ready, schedulable: n.Schedulable, allocatable: n.GPUAllocatable}
	}
	for _, p := range e.viewPods {
		cur.pods[p.Namespace+"/"+p.Name] = podDiff{phase: p.Phase, restarts: p.RestartCount}
	}
	for _, ev := range e.k8sEvents {
		cur.events[ev.ID] = ev.Count
	}

	prev := e.prev
	budget := maxActivityPerTick
	suppressed := 0
	log := func(severity, kind, title, detail string) {
		if budget <= 0 {
			suppressed++
			return
		}
		budget--
		e.addActivityLocked(now, severity, kind, title, detail)
	}

	// Sources
	for _, id := range []string{SourceHost, SourceNvidiaSMI, SourceAMDSMI, SourceDCGM, SourcePrometheus, SourceKubernetes} {
		st := e.src[id]
		if st == nil {
			continue
		}
		was, had := prev.sources[id]
		is := cur.sources[id]
		if had && was == is {
			continue
		}
		if is == StatePending || is == StateDisabled {
			continue
		}
		switch is {
		case StateConnected:
			title := "Source connected: " + st.status.Name
			if had && (was == StateUnreachable || was == StateError) {
				title = "Source recovered: " + st.status.Name
			}
			log("success", "source", title, st.status.Detail)
		case StateDegraded:
			log("warning", "source", "Source degraded: "+st.status.Name, st.status.Detail)
		case StateUnreachable, StateError:
			log("critical", "source", "Source unavailable: "+st.status.Name, strings.TrimSpace(st.status.Detail+" "+st.status.LastError))
		case StateNotFound:
			if !had || was == StatePending {
				log("info", "source", "Not found: "+st.status.Name, st.status.Detail)
			}
		}
	}

	// GPUs
	if !prev.initialized || len(prev.gpus) == 0 {
		if len(cur.gpus) > 0 {
			log("success", "gpu", fmt.Sprintf("%d GPU(s) discovered", len(cur.gpus)), e.snapshot.ScenarioDescription)
		}
	} else {
		for _, uuid := range sortedKeys(cur.gpus) {
			if _, ok := prev.gpus[uuid]; !ok {
				g := cur.gpus[uuid]
				log("info", "gpu", fmt.Sprintf("GPU %d on %s appeared", g.index, g.node), uuid)
			}
		}
	}
	for _, uuid := range sortedKeys(cur.gpus) {
		g := cur.gpus[uuid]
		p, ok := prev.gpus[uuid]
		if !ok {
			continue
		}
		label := fmt.Sprintf("GPU %d on %s", g.index, g.node)
		d := e.devices[uuid].dev
		if g.stale && !p.stale {
			log("critical", "gpu", label+" telemetry went stale", fmt.Sprintf("Last sample %.0fs ago (limit %.0fs).", d.StalenessSeconds, e.thresholds.MaxStalenessSeconds))
		} else if !g.stale && p.stale {
			log("success", "gpu", label+" telemetry is fresh again", uuid)
		}
		if g.hot && !p.hot {
			log("warning", "gpu", fmt.Sprintf("%s crossed %.0f°C", label, e.thresholds.TempWarningCelsius), fmt.Sprintf("Core temperature is now %.0f°C.", d.Latest.GPUTempCelsius))
		} else if !g.hot && p.hot && !g.stale {
			log("success", "gpu", fmt.Sprintf("%s cooled below %.0f°C", label, e.thresholds.TempWarningCelsius), fmt.Sprintf("Core temperature is now %.0f°C.", d.Latest.GPUTempCelsius))
		}
		if g.xid != 0 && g.xid != p.xid {
			log("critical", "gpu", fmt.Sprintf("%s reported Xid %d", label, g.xid), "A new NVIDIA driver error code was observed on this GPU.")
		}
		if g.throttle != p.throttle {
			if g.throttle != "" {
				log("warning", "gpu", label+" is being throttled", "Active clock event reasons: "+g.throttle)
			} else {
				log("success", "gpu", label+" is no longer throttled", "")
			}
		}
	}

	// Nodes
	if prev.initialized {
		for _, name := range sortedKeys(cur.nodes) {
			n := cur.nodes[name]
			p, ok := prev.nodes[name]
			if !ok {
				continue
			}
			if n.ready != p.ready {
				if n.ready {
					log("success", "node", "Node "+name+" is Ready again", "")
				} else {
					log("critical", "node", "Node "+name+" became NotReady", "")
				}
			}
			if n.schedulable != p.schedulable {
				if n.schedulable {
					log("success", "node", "Node "+name+" was uncordoned", "")
				} else {
					log("warning", "node", "Node "+name+" was cordoned", "")
				}
			}
			if n.allocatable != p.allocatable {
				sev := "warning"
				if n.allocatable > p.allocatable {
					sev = "success"
				}
				log(sev, "node", fmt.Sprintf("Node %s allocatable GPUs: %d → %d", name, p.allocatable, n.allocatable), "")
			}
		}
		// Workloads
		for _, key := range sortedKeys(cur.pods) {
			pod := cur.pods[key]
			p, ok := prev.pods[key]
			if !ok {
				if pod.phase == "Pending" {
					log("warning", "workload", "New pending GPU workload: "+key, "")
				} else {
					log("info", "workload", "GPU workload appeared: "+key, "Phase "+pod.phase)
				}
				continue
			}
			if pod.phase != p.phase {
				sev := "info"
				if pod.phase == "Failed" {
					sev = "critical"
				} else if pod.phase == "Running" {
					sev = "success"
				}
				log(sev, "workload", fmt.Sprintf("%s: %s → %s", key, p.phase, pod.phase), "")
			}
			if pod.restarts > p.restarts {
				log("critical", "workload", fmt.Sprintf("%s restarted (%d → %d)", key, p.restarts, pod.restarts), e.lastTerminationNote(key))
			}
		}
		for _, key := range sortedKeys(prev.pods) {
			if _, ok := cur.pods[key]; !ok {
				log("info", "workload", "GPU workload gone: "+key, "")
			}
		}
		// Kubernetes warning events
		for _, ev := range e.k8sEvents {
			if n, ok := prev.events[ev.ID]; ok && n >= ev.Count {
				continue
			}
			target := ev.InvolvedObjectName
			if ev.InvolvedObjectNamespace != "" {
				target = ev.InvolvedObjectNamespace + "/" + target
			}
			log("warning", "k8s_event", fmt.Sprintf("%s on %s %s", ev.Reason, strings.ToLower(ev.InvolvedObjectKind), target), truncate(ev.Message, 220))
		}
		if cur.fleet != prev.fleet && prev.fleet != "" {
			sev := "info"
			switch cur.fleet {
			case "critical", "telemetry_blind_spot", "no_telemetry":
				sev = "critical"
			case "warning":
				sev = "warning"
			case "healthy":
				sev = "success"
			}
			log(sev, "fleet", fmt.Sprintf("Fleet status: %s → %s", prev.fleet, cur.fleet), e.overview.FleetHealthHeadline)
		}
	}
	if suppressed > 0 {
		e.addActivityLocked(now, "info", "engine", fmt.Sprintf("%d more change(s) in this cycle", suppressed), "The change log is capped per cycle to stay readable.")
	}
	e.prev = cur
}

func (e *Engine) lastTerminationNote(key string) string {
	for _, p := range e.viewPods {
		if p.Namespace+"/"+p.Name == key && p.LastTermination != nil {
			return fmt.Sprintf("Last termination: %s (exit code %d).", orDash(p.LastTermination.Reason), p.LastTermination.ExitCode)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildViewsLocked prepares the per-GPU and per-process rows returned to the console.
func (e *Engine) buildViewsLocked(uuids []string, incidents []domain.Incident) {
	views := make([]GPUView, 0, len(uuids))
	procs := []ProcessView{}
	for _, uuid := range uuids {
		ds := e.devices[uuid]
		d := ds.dev
		v := GPUView{
			UUID: d.UUID, DeviceIndex: d.DeviceIndex, NodeName: d.NodeName, ModelName: d.ModelName, PCIBusID: d.PCIBusID,
			DriverVersion: d.DriverVersion, PowerLimitWatts: d.PowerLimitWatts, TotalMemoryMiB: d.TotalMemoryMiB,
			AllocatedNamespace: d.AllocatedNamespace, AllocatedPod: d.AllocatedPod, AllocatedContainer: d.AllocatedContainer,
			MappingStatus: string(d.MappingStatus), LastSampleAt: d.LastSampleAt, StalenessSeconds: round1(d.StalenessSeconds),
			IsStale: d.IsStale, Latest: d.Latest, Sources: append([]string{}, ds.extras.Sources...),
			FanSpeedPercent: ds.extras.FanSpeedPercent, PState: ds.extras.PState,
			ThrottleReasons: append([]string{}, ds.extras.ThrottleReasons...), PCIe: ds.extras.PCIe,
			Processes: append([]source.GPUProcess{}, ds.extras.Processes...), Emulated: ds.extras.Emulated,
			MIGProfile: ds.extras.MIGProfile, SampleCount: len(ds.samples), FirstSeenAt: ds.firstSeen,
			IncidentIDs: []string{},
		}
		if d.TotalMemoryMiB <= 0 && d.Latest.FBUsedMiB+d.Latest.FBFreeMiB > 0 {
			v.TotalMemoryMiB = d.Latest.FBUsedMiB + d.Latest.FBFreeMiB
		}
		exp := map[string]bool{}
		for _, o := range ds.obs {
			for k, ok := range o.exported {
				if ok {
					exp[k] = true
				}
			}
		}
		v.ExportedMetrics = sortedKeys(exp)
		if fb, ok := fbPercent(d); ok {
			r := round1(fb)
			v.FBUsedPercent = &r
		}
		worst := 0
		for _, inc := range incidents {
			hit := false
			for _, r := range inc.AffectedResources {
				if strings.Contains(r, uuid) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			v.IncidentIDs = append(v.IncidentIDs, inc.ID)
			if rank := severityRank(inc.Severity); rank > worst {
				worst = rank
				v.WorstSeverity = string(inc.Severity)
			}
		}
		views = append(views, v)
		for _, p := range ds.extras.Processes {
			procs = append(procs, ProcessView{GPUUUID: uuid, GPUIndex: d.DeviceIndex, NodeName: d.NodeName, PID: p.PID, Name: p.Name, UsedMemoryMiB: p.UsedMemoryMiB})
		}
	}
	e.gpuViews = views
	e.processes = procs
}
