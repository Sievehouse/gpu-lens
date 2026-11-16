package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/domain"
)

// PrometheusSource queries a Prometheus HTTP API v1 endpoint using strictly read-only GET requests.
type PrometheusSource struct {
	endpoint         string
	bearerToken      string
	maxResponseBytes int64
	mapping          config.MetricMappingConfig
	thresholds       config.RuleThresholds
	client           *http.Client
	guard            *ReadOnlyRoundTripper
}

// NewPrometheusSource constructs a read-only Prometheus HTTP API adapter.
func NewPrometheusSource(endpoint, bearerToken string, timeout time.Duration, maxBytes int64, mapping config.MetricMappingConfig, th config.RuleThresholds, baseTransport http.RoundTripper) *PrometheusSource {
	guard := NewReadOnlyRoundTripper(baseTransport)
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024
	}
	return &PrometheusSource{
		endpoint:         strings.TrimRight(endpoint, "/"),
		bearerToken:      bearerToken,
		maxResponseBytes: maxBytes,
		mapping:          mapping,
		thresholds:       th,
		guard:            guard,
		client: &http.Client{
			Timeout:   timeout,
			Transport: guard,
		},
	}
}

func (ps *PrometheusSource) ID() string   { return "prometheus-readonly" }
func (ps *PrometheusSource) Name() string { return "Prometheus HTTP API v1 (Read-Only)" }

type promVectorResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"` // [ unix_timestamp_float, "scalar_string" ]
		} `json:"result"`
	} `json:"data"`
}

// Collect queries configured DCGM metric names via GET /api/v1/query and normalizes the result.
// Missing required vs optional metrics are explicitly audited on the returned SourceHealth record.
func (ps *PrometheusSource) Collect(ctx context.Context) (*CollectorResult, error) {
	now := time.Now().UTC()
	requiredMetrics := []string{
		ps.mapping.GPUUtilMetric,
		ps.mapping.MemCopyUtilMetric,
		ps.mapping.FBUsedMetric,
		ps.mapping.FBFreeMetric,
		ps.mapping.GPUTempMetric,
		ps.mapping.MemoryTempMetric,
		ps.mapping.PowerUsageMetric,
		ps.mapping.XIDErrorsMetric,
		ps.mapping.PCIeReplayMetric,
		ps.mapping.RowRemapFailureMetric,
	}
	optionalMetrics := []string{
		ps.mapping.ECCDBEVolTotalMetric,
		ps.mapping.ECCSBEVolTotalMetric,
		ps.mapping.NVLinkCRCFlitErrorMetric,
		ps.mapping.NVLinkReplayErrorMetric,
		ps.mapping.ThermalViolationMetric,
		ps.mapping.PowerViolationMetric,
		ps.mapping.ExporterHealthStatusMetric,
	}

	allQueries := append(append([]string{}, requiredMetrics...), optionalMetrics...)
	queryExpr := fmt.Sprintf("{__name__=~%q}", strings.Join(allQueries, "|"))

	u, err := url.Parse(ps.endpoint + "/api/v1/query")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", queryExpr)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if ps.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+ps.bearerToken)
	}

	resp, err := ps.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus query failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus GET /api/v1/query returned HTTP %d", resp.StatusCode)
	}

	var parsed promVectorResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, ps.maxResponseBytes)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("prometheus decode json: %w", err)
	}

	var series []ParsedSeries
	for _, item := range parsed.Data.Result {
		name := item.Metric["__name__"]
		if name == "" || len(item.Value) < 2 {
			continue
		}
		tsFloat, _ := item.Value[0].(float64)
		valStr, _ := item.Value[1].(string)
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			continue
		}
		ts := now
		if tsFloat > 0 {
			sec := int64(tsFloat)
			nsec := int64((tsFloat - float64(sec)) * 1e9)
			ts = time.Unix(sec, nsec).UTC()
		}
		series = append(series, ParsedSeries{
			MetricName: name,
			Labels:     item.Metric,
			Value:      val,
			Timestamp:  ts,
		})
	}

	gpus, exported := NormalizeSeriesToGPUs(series, ps.mapping, ps.thresholds, now)

	var missingReq, missingOpt []string
	reqCount, optCount := 0, 0
	for _, m := range requiredMetrics {
		if exported[m] {
			reqCount++
		} else {
			missingReq = append(missingReq, m)
		}
	}
	for _, m := range optionalMetrics {
		if exported[m] {
			optCount++
		} else {
			missingOpt = append(missingOpt, m)
		}
	}

	snap := &domain.ClusterSnapshot{
		ClusterName:     "live-prometheus-cluster",
		DataMode:        "live_telemetry",
		CollectedAt:     now,
		GPUs:            gpus,
		ExportedMetrics: exported,
		Sources: []domain.SourceHealth{
			{
				ID:                    ps.ID(),
				Name:                  ps.Name(),
				Kind:                  "prometheus",
				Mode:                  "live",
				Endpoint:              ps.endpoint,
				Available:             true,
				ReadOnlyVerified:      true,
				LastSuccessAt:         now,
				StalenessSeconds:      0,
				IsStale:               false,
				ExportedRequiredCount: reqCount,
				ExportedOptionalCount: optCount,
				MissingRequired:       missingReq,
				MissingOptional:       missingOpt,
			},
		},
	}
	return &CollectorResult{Snapshot: snap}, nil
}

// ---------------------------------------------------------------------------------------------
// Live console support: richer read-only queries (still GET-only through ReadOnlyRoundTripper)
// ---------------------------------------------------------------------------------------------

// Endpoint returns the configured Prometheus base URL.
func (ps *PrometheusSource) Endpoint() string { return ps.endpoint }

// Guard exposes the read-only transport guard for auditing.
func (ps *PrometheusSource) Guard() *ReadOnlyRoundTripper { return ps.guard }

// SetBearerToken replaces the bearer token used for subsequent requests (for example after a token file rotated).
func (ps *PrometheusSource) SetBearerToken(token string) { ps.bearerToken = token }

type promAPIResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type promVectorItem struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}

type promMatrixItem struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

func (ps *PrometheusSource) apiGet(ctx context.Context, path string, params url.Values, dest interface{}) error {
	u, err := url.Parse(ps.endpoint + path)
	if err != nil {
		return err
	}
	if params != nil {
		u.RawQuery = params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if ps.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+ps.bearerToken)
	}
	resp, err := ps.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return fmt.Errorf("GET %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, ps.maxResponseBytes)).Decode(dest); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func (ps *PrometheusSource) instantQuery(ctx context.Context, expr string) (*promAPIResponse, error) {
	var out promAPIResponse
	if err := ps.apiGet(ctx, "/api/v1/query", url.Values{"query": {expr}}, &out); err != nil {
		return nil, err
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus query error (%s)", out.ErrorType)
	}
	return &out, nil
}

func promSampleValue(pair []interface{}) (ts time.Time, val float64, ok bool) {
	if len(pair) < 2 {
		return time.Time{}, 0, false
	}
	tsFloat, okTS := pair[0].(float64)
	valStr, okVal := pair[1].(string)
	if !okTS || !okVal {
		return time.Time{}, 0, false
	}
	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return time.Time{}, 0, false
	}
	sec := int64(tsFloat)
	return time.Unix(sec, int64((tsFloat-float64(sec))*1e9)).UTC(), v, true
}

func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || r == ':'
		if !(isLetter || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func (ps *PrometheusSource) liveMetricNames(m config.MetricMappingConfig) []string {
	var names []string
	seen := map[string]bool{}
	for _, n := range append(append(RequiredDCGMMetrics(m), OptionalDCGMMetrics(m)...),
		dcgmPowerLimitMetric, dcgmFanSpeedMetric, dcgmFBTotalMetric, dcgmFBReservedMetric, LabEmulatorMarkerMetric) {
		if validMetricName(n) && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return names
}

// PromLiveResult is the outcome of one live collection from Prometheus.
type PromLiveResult struct {
	Normalized      LiveNormalizeResult
	Version         string
	SkewSeconds     *float64
	TargetsKnown    bool
	TargetsUp       int
	TargetsDown     int
	DownTargets     []string
	TimestampsKnown bool
	Queries         int
}

// CollectLive reads the current DCGM series through Prometheus and audits scrape health around them.
// Only the main metric query is required to succeed; version, clock-skew, sample-timestamp and target-health
// lookups are best effort so that Prometheus-compatible backends without those APIs still work.
func (ps *PrometheusSource) CollectLive(ctx context.Context, mapping config.MetricMappingConfig, maxStaleness float64, knownNodes map[string]bool, withAudit bool) (*PromLiveResult, error) {
	res := &PromLiveResult{}
	names := ps.liveMetricNames(mapping)
	main, err := ps.instantQuery(ctx, fmt.Sprintf("{__name__=~%q}", strings.Join(names, "|")))
	res.Queries++
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var items []promVectorItem
	if main.Data.ResultType == "vector" {
		if err := json.Unmarshal(main.Data.Result, &items); err != nil {
			return nil, fmt.Errorf("decode prometheus vector: %w", err)
		}
	}
	var series []ParsedSeries
	for _, it := range items {
		name := it.Metric["__name__"]
		ts, val, ok := promSampleValue(it.Value)
		if name == "" || !ok {
			continue
		}
		series = append(series, ParsedSeries{MetricName: name, Labels: it.Metric, Value: val, Timestamp: ts})
	}

	// Instant-vector samples are stamped with the evaluation time, so ask Prometheus for the real scrape time.
	sampleTS := map[string]time.Time{}
	if validMetricName(mapping.GPUUtilMetric) {
		if tsResp, err := ps.instantQuery(ctx, fmt.Sprintf("timestamp(%s)", mapping.GPUUtilMetric)); err == nil && tsResp.Data.ResultType == "vector" {
			res.Queries++
			var tsItems []promVectorItem
			if json.Unmarshal(tsResp.Data.Result, &tsItems) == nil {
				for _, it := range tsItems {
					uuid := firstLabel(it.Metric, mapping.UUIDLabelCandidates)
					if uuid == "" {
						continue
					}
					if mig := it.Metric["GPU_I_ID"]; mig != "" {
						uuid = uuid + "/mig-" + mig
					}
					if _, v, ok := promSampleValue(it.Value); ok && v > 0 {
						sec := int64(v)
						sampleTS[uuid] = time.Unix(sec, int64((v-float64(sec))*1e9)).UTC()
						res.TimestampsKnown = true
					}
				}
			}
		}
	}

	res.Normalized = NormalizeSeriesLive(series, LiveNormalizeOptions{
		Mapping:             mapping,
		MaxStalenessSeconds: maxStaleness,
		Now:                 now,
		KnownNodeNames:      knownNodes,
		SampleTimestamps:    sampleTS,
		SourceTag:           "prometheus",
	})

	if !withAudit {
		return res, nil
	}

	if tResp, err := ps.instantQuery(ctx, "time()"); err == nil && tResp.Data.ResultType == "scalar" {
		res.Queries++
		var pair []interface{}
		if json.Unmarshal(tResp.Data.Result, &pair) == nil {
			if _, v, ok := promSampleValue(pair); ok {
				skew := float64(time.Now().UTC().UnixNano())/1e9 - v
				res.SkewSeconds = &skew
			}
		}
	}

	var build struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := ps.apiGet(ctx, "/api/v1/status/buildinfo", nil, &build); err == nil {
		res.Queries++
		res.Version = build.Data.Version
	}

	if upResp, err := ps.instantQuery(ctx, `up{job=~"(?i).*(dcgm|nvidia|gpu).*"}`); err == nil && upResp.Data.ResultType == "vector" {
		res.Queries++
		var upItems []promVectorItem
		if json.Unmarshal(upResp.Data.Result, &upItems) == nil {
			res.TargetsKnown = true
			for _, it := range upItems {
				_, v, ok := promSampleValue(it.Value)
				if !ok {
					continue
				}
				if v >= 1 {
					res.TargetsUp++
				} else {
					res.TargetsDown++
					if len(res.DownTargets) < 8 {
						res.DownTargets = append(res.DownTargets, fmt.Sprintf("%s/%s", it.Metric["job"], it.Metric["instance"]))
					}
				}
			}
		}
	}
	return res, nil
}

// Backfill reads up to `minutes` of history for the core utilization, memory, temperature, power and XID series
// with one bounded GET /api/v1/query_range, so live rules that need an observation window do not start blind.
func (ps *PrometheusSource) Backfill(ctx context.Context, mapping config.MetricMappingConfig, minutes float64, stepSeconds int) (map[string][]domain.GPUMetricPoint, error) {
	if minutes <= 0 {
		return nil, nil
	}
	if stepSeconds < 15 {
		stepSeconds = 15
	}
	var names []string
	for _, n := range []string{mapping.GPUUtilMetric, mapping.MemCopyUtilMetric, mapping.FBUsedMetric, mapping.FBFreeMetric,
		mapping.GPUTempMetric, mapping.MemoryTempMetric, mapping.PowerUsageMetric, mapping.XIDErrorsMetric} {
		if validMetricName(n) {
			names = append(names, n)
		}
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(minutes * float64(time.Minute)))
	params := url.Values{
		"query": {fmt.Sprintf("{__name__=~%q}", strings.Join(names, "|"))},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.Itoa(stepSeconds)},
	}
	var out promAPIResponse
	if err := ps.apiGet(ctx, "/api/v1/query_range", params, &out); err != nil {
		return nil, err
	}
	if out.Status != "success" || out.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus range query returned %s/%s", out.Status, out.Data.ResultType)
	}
	var items []promMatrixItem
	if err := json.Unmarshal(out.Data.Result, &items); err != nil {
		return nil, fmt.Errorf("decode prometheus matrix: %w", err)
	}
	byDev := map[string]map[int64]*domain.GPUMetricPoint{}
	for _, it := range items {
		uuid := firstLabel(it.Metric, mapping.UUIDLabelCandidates)
		name := it.Metric["__name__"]
		if uuid == "" || name == "" {
			continue
		}
		if mig := it.Metric["GPU_I_ID"]; mig != "" {
			uuid = uuid + "/mig-" + mig
		}
		points := byDev[uuid]
		if points == nil {
			points = map[int64]*domain.GPUMetricPoint{}
			byDev[uuid] = points
		}
		for _, pair := range it.Values {
			ts, val, ok := promSampleValue(pair)
			if !ok {
				continue
			}
			pt := points[ts.Unix()]
			if pt == nil {
				pt = &domain.GPUMetricPoint{Timestamp: ts}
				points[ts.Unix()] = pt
			}
			switch name {
			case mapping.GPUUtilMetric:
				pt.GPUUtilPercent = val
			case mapping.MemCopyUtilMetric:
				pt.MemCopyUtilPercent = val
			case mapping.FBUsedMetric:
				pt.FBUsedMiB = val
			case mapping.FBFreeMetric:
				pt.FBFreeMiB = val
			case mapping.GPUTempMetric:
				pt.GPUTempCelsius = val
			case mapping.MemoryTempMetric:
				pt.MemoryTempCelsius = val
			case mapping.PowerUsageMetric:
				pt.PowerUsageWatts = val
			case mapping.XIDErrorsMetric:
				pt.XIDErrorCode = int(val)
			}
		}
	}
	result := make(map[string][]domain.GPUMetricPoint, len(byDev))
	for uuid, points := range byDev {
		keys := make([]int64, 0, len(points))
		for k := range points {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		list := make([]domain.GPUMetricPoint, 0, len(keys))
		for _, k := range keys {
			list = append(list, *points[k])
		}
		result[uuid] = list
	}
	return result, nil
}
