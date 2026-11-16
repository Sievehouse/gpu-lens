package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

// LabEmulatorMarkerMetric is exported only by the GPU Lens Live Lab emulator (scripts/live_lab.py). Any source
// that exposes it is flagged as emulated so synthetic telemetry can never be mistaken for real hardware.
const LabEmulatorMarkerMetric = "gpulens_lab_emulator_info"

// Additional DCGM fields used by the live console when the exporter is configured to publish them.
const (
	dcgmPowerLimitMetric = "DCGM_FI_DEV_POWER_MGMT_LIMIT"
	dcgmFanSpeedMetric   = "DCGM_FI_DEV_FAN_SPEED"
	dcgmFBTotalMetric    = "DCGM_FI_DEV_FB_TOTAL"
	dcgmFBReservedMetric = "DCGM_FI_DEV_FB_RESERVED"
)

// GPUProcess is a compute process holding a GPU context, as reported by the local NVIDIA driver.
type GPUProcess struct {
	PID           int      `json:"pid"`
	Name          string   `json:"name"`
	UsedMemoryMiB *float64 `json:"used_memory_mib"`
}

// PCIeLink describes the negotiated PCIe link of a GPU versus the maximum it supports in this system.
type PCIeLink struct {
	GenCurrent   int `json:"gen_current"`
	GenMax       int `json:"gen_max"`
	WidthCurrent int `json:"width_current"`
	WidthMax     int `json:"width_max"`
}

// GPUExtras carries live-only device details that are not part of the rule-engine domain model.
type GPUExtras struct {
	Sources         []string     `json:"sources"`
	FanSpeedPercent *float64     `json:"fan_speed_percent"`
	PState          string       `json:"pstate,omitempty"`
	ThrottleReasons []string     `json:"throttle_reasons"`
	PCIe            *PCIeLink    `json:"pcie"`
	Processes       []GPUProcess `json:"processes"`
	Emulated        bool         `json:"emulated"`
	MIGProfile      string       `json:"mig_profile,omitempty"`
}

// RequiredDCGMMetrics lists the default dcgm-exporter fields GPU Lens needs for full rule coverage.
func RequiredDCGMMetrics(m config.MetricMappingConfig) []string {
	return []string{
		m.GPUUtilMetric, m.MemCopyUtilMetric, m.FBUsedMetric, m.FBFreeMetric, m.GPUTempMetric,
		m.MemoryTempMetric, m.PowerUsageMetric, m.XIDErrorsMetric, m.PCIeReplayMetric, m.RowRemapFailureMetric,
	}
}

// OptionalDCGMMetrics lists fields that are commented out in the stock dcgm-exporter collector CSV.
func OptionalDCGMMetrics(m config.MetricMappingConfig) []string {
	return []string{
		m.UncorrectableRemapMetric, m.ECCDBEVolTotalMetric, m.ECCSBEVolTotalMetric, m.NVLinkCRCFlitErrorMetric,
		m.NVLinkReplayErrorMetric, m.ThermalViolationMetric, m.PowerViolationMetric, m.ExporterHealthStatusMetric,
		m.ClockEventsCountMetric,
	}
}

// LiveMetricMapping extends the default mapping with label conventions seen on real Prometheus installs.
// When Prometheus scrapes dcgm-exporter with honor_labels=false, the workload labels are renamed to
// exported_pod / exported_namespace / exported_container, and the plain pod label names the exporter itself.
func LiveMetricMapping(base config.MetricMappingConfig) config.MetricMappingConfig {
	m := base
	m.PodNameLabelCandidates = prependUnique([]string{"exported_pod"}, base.PodNameLabelCandidates)
	m.NamespaceLabelCandidates = prependUnique([]string{"exported_namespace"}, base.NamespaceLabelCandidates)
	m.ContainerLabelCandidates = prependUnique([]string{"exported_container"}, base.ContainerLabelCandidates)
	return m
}

func prependUnique(front, rest []string) []string {
	out := append([]string(nil), front...)
	for _, r := range rest {
		dup := false
		for _, o := range out {
			if o == r {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// ParseDCGMExpositionLenient parses Prometheus text exposition and skips malformed lines instead of failing the
// whole scrape. It returns the parsed series and the number of lines it had to skip.
func ParseDCGMExpositionLenient(body string, fallbackTime time.Time) ([]ParsedSeries, int, bool) {
	var out []ParsedSeries
	skipped := 0
	explicitTS := false
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, err := parsePrometheusLine(line, fallbackTime)
		if err != nil {
			skipped++
			continue
		}
		if !series.Timestamp.Equal(fallbackTime) {
			explicitTS = true
		}
		out = append(out, series)
	}
	return out, skipped, explicitTS
}

// LiveNormalizeOptions controls how raw DCGM series become normalized GPU devices for the live console.
type LiveNormalizeOptions struct {
	Mapping             config.MetricMappingConfig
	MaxStalenessSeconds float64
	Now                 time.Time
	// KnownNodeNames, when non-empty, makes the normalizer prefer the node label whose value is a known
	// Kubernetes node name (handles exporters whose Hostname label is the exporter pod name).
	KnownNodeNames map[string]bool
	// DefaultNodeName is used when a series carries no node label at all (single-host direct scrape).
	DefaultNodeName string
	// SampleTimestamps overrides the per-device sample time (Prometheus `timestamp()` results keyed by UUID).
	SampleTimestamps map[string]time.Time
	SourceTag        string
}

// LiveNormalizeResult is the outcome of NormalizeSeriesLive.
type LiveNormalizeResult struct {
	GPUs               []domain.GPUDevice
	Extras             map[string]*GPUExtras
	Exported           map[string]bool
	SeriesCount        int
	GPUSeriesCount     int
	IgnoredSelfLabels  int
	HonorLabelsRenamed bool
	MIGInstances       int
	Emulated           bool
	MappedGPUs         int
}

func looksLikeExporterSelfLabel(pod, container string) bool {
	p := strings.ToLower(pod)
	c := strings.ToLower(container)
	return strings.Contains(p, "dcgm-exporter") || strings.Contains(c, "dcgm-exporter")
}

// NormalizeSeriesLive converts DCGM series (scraped directly or read back from Prometheus) into GPU devices.
// Unlike NormalizeSeriesToGPUs it never substitutes model-specific defaults: unknown power limits and memory
// sizes stay zero, and optional metrics stay nil.
func NormalizeSeriesLive(series []ParsedSeries, opt LiveNormalizeOptions) LiveNormalizeResult {
	m := opt.Mapping
	res := LiveNormalizeResult{
		Extras:      make(map[string]*GPUExtras),
		Exported:    make(map[string]bool),
		SeriesCount: len(series),
	}

	for _, s := range series {
		if s.Labels["exported_pod"] != "" || s.Labels["exported_namespace"] != "" || s.Labels["exported_container"] != "" {
			res.HonorLabelsRenamed = true
			break
		}
	}
	// With honor_labels=false every plain pod/namespace/container label names the exporter, so only the
	// exported_* variants may be used for workload attribution.
	podCands, nsCands, contCands := m.PodNameLabelCandidates, m.NamespaceLabelCandidates, m.ContainerLabelCandidates
	if res.HonorLabelsRenamed {
		podCands, nsCands, contCands = []string{"exported_pod"}, []string{"exported_namespace"}, []string{"exported_container"}
	}

	type devState struct {
		dev        *domain.GPUDevice
		fbTotal    float64
		fbReserved float64
		selfLabel  bool
	}
	byKey := make(map[string]*devState)
	var order []string

	for _, s := range series {
		res.Exported[s.MetricName] = true
		if s.MetricName == LabEmulatorMarkerMetric {
			res.Emulated = true
			continue
		}
		uuid := firstLabel(s.Labels, m.UUIDLabelCandidates)
		if uuid == "" {
			continue
		}
		res.GPUSeriesCount++
		key := uuid
		migID := s.Labels["GPU_I_ID"]
		if migID != "" {
			key = uuid + "/mig-" + migID
		}
		st, ok := byKey[key]
		if !ok {
			idx, _ := strconv.Atoi(s.Labels[m.GPUIndexLabel])
			nodeName := pickNodeName(s.Labels, m.NodeNameLabelCandidates, opt.KnownNodeNames)
			if nodeName == "" {
				nodeName = opt.DefaultNodeName
			}
			dev := &domain.GPUDevice{
				UUID:          key,
				DeviceIndex:   idx,
				NodeName:      nodeName,
				ModelName:     firstLabel(s.Labels, m.ModelNameLabelCandidates),
				PCIBusID:      s.Labels["pci_bus_id"],
				DriverVersion: s.Labels["DCGM_FI_DRIVER_VERSION"],
				MappingStatus: domain.MappingUnallocated,
				LastSampleAt:  s.Timestamp,
				Latest:        domain.GPUMetricPoint{Timestamp: s.Timestamp},
			}
			st = &devState{dev: dev}
			byKey[key] = st
			order = append(order, key)
			ex := &GPUExtras{Sources: []string{opt.SourceTag}, ThrottleReasons: []string{}, Processes: []GPUProcess{}}
			if migID != "" {
				ex.MIGProfile = s.Labels["GPU_I_PROFILE"]
				res.MIGInstances++
			}
			res.Extras[key] = ex
		}
		dev := st.dev

		// Workload attribution labels can appear on any series of the device.
		if dev.AllocatedPod == "" {
			pod := firstLabel(s.Labels, podCands)
			ns := firstLabel(s.Labels, nsCands)
			cont := firstLabel(s.Labels, contCands)
			if pod != "" && ns != "" {
				if !res.HonorLabelsRenamed && looksLikeExporterSelfLabel(pod, cont) {
					st.selfLabel = true
				} else {
					dev.AllocatedPod, dev.AllocatedNamespace, dev.AllocatedContainer = pod, ns, cont
					dev.MappingStatus = domain.MappingMapped
				}
			}
		}
		if dev.ModelName == "" {
			dev.ModelName = firstLabel(s.Labels, m.ModelNameLabelCandidates)
		}
		if s.Timestamp.After(dev.LastSampleAt) {
			dev.LastSampleAt = s.Timestamp
			dev.Latest.Timestamp = s.Timestamp
		}

		switch s.MetricName {
		case m.GPUUtilMetric:
			dev.Latest.GPUUtilPercent = s.Value
		case m.MemCopyUtilMetric:
			dev.Latest.MemCopyUtilPercent = s.Value
		case m.FBUsedMetric:
			dev.Latest.FBUsedMiB = s.Value
		case m.FBFreeMetric:
			dev.Latest.FBFreeMiB = s.Value
		case m.GPUTempMetric:
			dev.Latest.GPUTempCelsius = s.Value
		case m.MemoryTempMetric:
			dev.Latest.MemoryTempCelsius = s.Value
		case m.PowerUsageMetric:
			dev.Latest.PowerUsageWatts = s.Value
		case m.XIDErrorsMetric:
			dev.Latest.XIDErrorCode = int(s.Value)
		case m.PCIeReplayMetric:
			dev.Latest.PCIeReplayCounter = int(s.Value)
		case m.RowRemapFailureMetric:
			dev.Latest.RowRemapFailure = int(s.Value)
		case m.UncorrectableRemapMetric:
			dev.Latest.UncorrectableRemappedRows = int(s.Value)
		case m.ECCDBEVolTotalMetric:
			v := int(s.Value)
			dev.Latest.ECCDBEVolTotal = &v
		case m.ECCSBEVolTotalMetric:
			v := int(s.Value)
			dev.Latest.ECCSBEVolTotal = &v
		case m.NVLinkCRCFlitErrorMetric:
			v := int(s.Value)
			dev.Latest.NVLinkCRCFlitErrors = &v
		case m.NVLinkReplayErrorMetric:
			v := int(s.Value)
			dev.Latest.NVLinkReplayErrors = &v
		case m.ThermalViolationMetric:
			v := int64(s.Value)
			dev.Latest.ThermalViolationNs = &v
		case m.PowerViolationMetric:
			v := int64(s.Value)
			dev.Latest.PowerViolationNs = &v
		case m.ExporterHealthStatusMetric:
			v := int(s.Value)
			dev.Latest.HealthStatus = &v
		case m.ClockEventsCountMetric:
			if s.Value > 0 && s.Labels["clock_event"] != "" {
				dev.Latest.ClockThrottleReason = s.Labels["clock_event"]
				ex := res.Extras[key]
				ex.ThrottleReasons = appendUnique(ex.ThrottleReasons, s.Labels["clock_event"])
			}
		case dcgmPowerLimitMetric:
			dev.PowerLimitWatts = s.Value
		case dcgmFanSpeedMetric:
			v := s.Value
			res.Extras[key].FanSpeedPercent = &v
		case dcgmFBTotalMetric:
			st.fbTotal = s.Value
		case dcgmFBReservedMetric:
			st.fbReserved = s.Value
		}
	}

	for _, key := range order {
		st := byKey[key]
		d := st.dev
		if st.selfLabel && d.AllocatedPod == "" {
			res.IgnoredSelfLabels++
		}
		switch {
		case st.fbTotal > 0:
			d.TotalMemoryMiB = st.fbTotal
		case d.Latest.FBUsedMiB+d.Latest.FBFreeMiB > 0:
			d.TotalMemoryMiB = d.Latest.FBUsedMiB + d.Latest.FBFreeMiB + st.fbReserved
		}
		if ts, ok := opt.SampleTimestamps[d.UUID]; ok && !ts.IsZero() {
			d.LastSampleAt = ts
			d.Latest.Timestamp = ts
		}
		d.StalenessSeconds = opt.Now.Sub(d.LastSampleAt).Seconds()
		if d.StalenessSeconds < 0 {
			d.StalenessSeconds = 0
		}
		d.IsStale = d.StalenessSeconds > opt.MaxStalenessSeconds
		if d.MappingStatus == domain.MappingMapped {
			res.MappedGPUs++
		}
		res.Extras[key].Emulated = res.Emulated
		res.GPUs = append(res.GPUs, *d)
	}
	sort.SliceStable(res.GPUs, func(i, j int) bool {
		if res.GPUs[i].NodeName != res.GPUs[j].NodeName {
			return res.GPUs[i].NodeName < res.GPUs[j].NodeName
		}
		if res.GPUs[i].DeviceIndex != res.GPUs[j].DeviceIndex {
			return res.GPUs[i].DeviceIndex < res.GPUs[j].DeviceIndex
		}
		return res.GPUs[i].UUID < res.GPUs[j].UUID
	})
	return res
}

func pickNodeName(labels map[string]string, candidates []string, known map[string]bool) string {
	if len(known) > 0 {
		for _, k := range candidates {
			if v := labels[k]; v != "" && known[v] {
				return v
			}
		}
	}
	return firstLabel(labels, candidates)
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// MissingMetrics splits the wanted metric names into those present and absent in `exported`.
func MissingMetrics(wanted []string, exported map[string]bool) (present int, missing []string) {
	for _, name := range wanted {
		if name == "" {
			continue
		}
		if exported[name] {
			present++
		} else {
			missing = append(missing, name)
		}
	}
	return present, missing
}

// DCGMExporterSource scrapes a dcgm-exporter /metrics endpoint directly with a read-only GET.
type DCGMExporterSource struct {
	endpoint         string
	maxResponseBytes int64
	client           *http.Client
	guard            *ReadOnlyRoundTripper
}

// NewDCGMExporterSource constructs a direct dcgm-exporter scraper.
func NewDCGMExporterSource(endpoint string, timeout time.Duration, maxBytes int64, baseTransport http.RoundTripper) *DCGMExporterSource {
	guard := NewReadOnlyRoundTripper(baseTransport)
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024
	}
	return &DCGMExporterSource{
		endpoint:         endpoint,
		maxResponseBytes: maxBytes,
		guard:            guard,
		client:           &http.Client{Timeout: timeout, Transport: guard},
	}
}

func (ds *DCGMExporterSource) ID() string       { return "dcgm_exporter" }
func (ds *DCGMExporterSource) Name() string     { return "DCGM Exporter (direct /metrics scrape)" }
func (ds *DCGMExporterSource) Endpoint() string { return ds.endpoint }

// Guard exposes the read-only transport guard for auditing.
func (ds *DCGMExporterSource) Guard() *ReadOnlyRoundTripper { return ds.guard }

// DCGMScrape is the raw outcome of one scrape.
type DCGMScrape struct {
	Series         []ParsedSeries
	SkippedLines   int
	ExplicitTS     bool
	Bytes          int
	ScrapedAt      time.Time
	Truncated      bool
	ContentType    string
	EmulatorHeader string
}

// Scrape fetches and parses the exposition text.
func (ds *DCGMExporterSource) Scrape(ctx context.Context) (*DCGMScrape, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ds.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain;version=0.0.4")
	resp, err := ds.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned HTTP %d", ds.endpoint, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ds.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read exposition body: %w", err)
	}
	truncated := int64(len(raw)) > ds.maxResponseBytes
	if truncated {
		raw = raw[:ds.maxResponseBytes]
	}
	now := time.Now().UTC()
	series, skipped, explicit := ParseDCGMExpositionLenient(string(raw), now)
	return &DCGMScrape{
		Series:         series,
		SkippedLines:   skipped,
		ExplicitTS:     explicit,
		Bytes:          len(raw),
		ScrapedAt:      now,
		Truncated:      truncated,
		ContentType:    resp.Header.Get("Content-Type"),
		EmulatorHeader: resp.Header.Get("X-GPULens-Emulated"),
	}, nil
}
