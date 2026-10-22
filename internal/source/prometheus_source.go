package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
