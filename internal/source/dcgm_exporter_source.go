// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// ParsedSeries represents a single parsed Prometheus / DCGM Exporter metric sample with labels.
type ParsedSeries struct {
	MetricName string
	Labels     map[string]string
	Value      float64
	Timestamp  time.Time
}

// ParseDCGMExposition parses standard Prometheus text exposition format emitted by dcgm-exporter (/metrics).
// It ignores HELP/TYPE comments and safely parses metric name, label sets, scalar value, and optional millisecond timestamp.
func ParseDCGMExposition(body string, fallbackTime time.Time) ([]ParsedSeries, error) {
	var out []ParsedSeries
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, err := parsePrometheusLine(line, fallbackTime)
		if err != nil {
			return nil, err
		}
		out = append(out, series)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parsePrometheusLine(line string, fallbackTime time.Time) (ParsedSeries, error) {
	labels := make(map[string]string)
	var metricName, rest string

	braceOpen := strings.IndexByte(line, '{')
	if braceOpen != -1 {
		braceClose := strings.LastIndexByte(line, '}')
		if braceClose == -1 || braceClose < braceOpen {
			return ParsedSeries{}, fmt.Errorf("malformed label block in line: %q", line)
		}
		metricName = strings.TrimSpace(line[:braceOpen])
		labelBlock := line[braceOpen+1 : braceClose]
		rest = strings.TrimSpace(line[braceClose+1:])
		if err := parseLabels(labelBlock, labels); err != nil {
			return ParsedSeries{}, err
		}
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return ParsedSeries{}, fmt.Errorf("malformed metric line: %q", line)
		}
		metricName = fields[0]
		rest = strings.Join(fields[1:], " ")
	}

	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return ParsedSeries{}, fmt.Errorf("missing value in metric line: %q", line)
	}
	val, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return ParsedSeries{}, fmt.Errorf("invalid float value %q in line %q: %w", fields[0], line, err)
	}
	ts := fallbackTime
	if len(fields) >= 2 {
		if ms, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			ts = time.UnixMilli(ms).UTC()
		}
	}
	return ParsedSeries{
		MetricName: metricName,
		Labels:     labels,
		Value:      val,
		Timestamp:  ts,
	}, nil
}

func parseLabels(block string, dest map[string]string) error {
	i := 0
	n := len(block)
	for i < n {
		for i < n && (block[i] == ' ' || block[i] == ',') {
			i++
		}
		if i >= n {
			break
		}
		eq := strings.IndexByte(block[i:], '=')
		if eq == -1 {
			return fmt.Errorf("missing '=' in label block %q", block)
		}
		key := strings.TrimSpace(block[i : i+eq])
		i += eq + 1
		if i >= n || block[i] != '"' {
			return fmt.Errorf("expected quoted value for label %q", key)
		}
		i++ // skip opening quote
		var val strings.Builder
		for i < n && block[i] != '"' {
			if block[i] == '\\' && i+1 < n {
				i++
			}
			val.WriteByte(block[i])
			i++
		}
		if i >= n || block[i] != '"' {
			return fmt.Errorf("unterminated quote for label %q", key)
		}
		i++ // skip closing quote
		dest[key] = val.String()
	}
	return nil
}

// NormalizeSeriesToGPUs converts parsed Prometheus/DCGM series into normalized GPUDevice records
// using configurable metric and label mappings. Optional metrics that are not present in the input
// remain nil (never fabricated as zero).
func NormalizeSeriesToGPUs(series []ParsedSeries, mapping config.MetricMappingConfig, th config.RuleThresholds, now time.Time) ([]domain.GPUDevice, map[string]bool) {
	exported := make(map[string]bool)
	byUUID := make(map[string]*domain.GPUDevice)
	var order []string

	for _, s := range series {
		exported[s.MetricName] = true
		uuid := firstLabel(s.Labels, mapping.UUIDLabelCandidates)
		if uuid == "" {
			continue
		}
		dev, ok := byUUID[uuid]
		if !ok {
			idx, _ := strconv.Atoi(s.Labels[mapping.GPUIndexLabel])
			nodeName := firstLabel(s.Labels, mapping.NodeNameLabelCandidates)
			modelName := firstLabel(s.Labels, mapping.ModelNameLabelCandidates)
			podName := firstLabel(s.Labels, mapping.PodNameLabelCandidates)
			nsName := firstLabel(s.Labels, mapping.NamespaceLabelCandidates)
			contName := firstLabel(s.Labels, mapping.ContainerLabelCandidates)

			mapStatus := domain.MappingUnallocated
			if podName != "" && nsName != "" {
				mapStatus = domain.MappingMapped
			} else if s.Labels["allocated"] == "true" {
				mapStatus = domain.MappingMissingPodLabels
			}

			dev = &domain.GPUDevice{
				UUID:               uuid,
				DeviceIndex:        idx,
				NodeName:           nodeName,
				ModelName:          modelName,
				PCIBusID:           s.Labels["pci_bus_id"],
				DriverVersion:      s.Labels["DCGM_FI_DRIVER_VERSION"],
				PowerLimitWatts:    700.0,
				TotalMemoryMiB:     81920.0,
				AllocatedNamespace: nsName,
				AllocatedPod:       podName,
				AllocatedContainer: contName,
				MappingStatus:      mapStatus,
				LastSampleAt:       s.Timestamp,
				Latest: domain.GPUMetricPoint{
					Timestamp: s.Timestamp,
				},
			}
			byUUID[uuid] = dev
			order = append(order, uuid)
		}

		if s.Timestamp.After(dev.LastSampleAt) {
			dev.LastSampleAt = s.Timestamp
			dev.Latest.Timestamp = s.Timestamp
		}

		// Map default and optional DCGM fields
		switch s.MetricName {
		case mapping.GPUUtilMetric:
			dev.Latest.GPUUtilPercent = s.Value
		case mapping.MemCopyUtilMetric:
			dev.Latest.MemCopyUtilPercent = s.Value
		case mapping.FBUsedMetric:
			dev.Latest.FBUsedMiB = s.Value
		case mapping.FBFreeMetric:
			dev.Latest.FBFreeMiB = s.Value
		case mapping.GPUTempMetric:
			dev.Latest.GPUTempCelsius = s.Value
		case mapping.MemoryTempMetric:
			dev.Latest.MemoryTempCelsius = s.Value
		case mapping.PowerUsageMetric:
			dev.Latest.PowerUsageWatts = s.Value
		case mapping.XIDErrorsMetric:
			dev.Latest.XIDErrorCode = int(s.Value)
		case mapping.PCIeReplayMetric:
			dev.Latest.PCIeReplayCounter = int(s.Value)
		case mapping.RowRemapFailureMetric:
			dev.Latest.RowRemapFailure = int(s.Value)
		case mapping.UncorrectableRemapMetric:
			dev.Latest.UncorrectableRemappedRows = int(s.Value)
		case mapping.ECCDBEVolTotalMetric:
			v := int(s.Value)
			dev.Latest.ECCDBEVolTotal = &v
		case mapping.ECCSBEVolTotalMetric:
			v := int(s.Value)
			dev.Latest.ECCSBEVolTotal = &v
		case mapping.NVLinkCRCFlitErrorMetric:
			v := int(s.Value)
			dev.Latest.NVLinkCRCFlitErrors = &v
		case mapping.NVLinkReplayErrorMetric:
			v := int(s.Value)
			dev.Latest.NVLinkReplayErrors = &v
		case mapping.ThermalViolationMetric:
			v := int64(s.Value)
			dev.Latest.ThermalViolationNs = &v
		case mapping.PowerViolationMetric:
			v := int64(s.Value)
			dev.Latest.PowerViolationNs = &v
		case mapping.ExporterHealthStatusMetric:
			v := int(s.Value)
			dev.Latest.HealthStatus = &v
		case mapping.ClockEventsCountMetric:
			if s.Value > 0 && s.Labels["clock_event"] != "" {
				dev.Latest.ClockThrottleReason = s.Labels["clock_event"]
			}
		}
	}

	out := make([]domain.GPUDevice, 0, len(order))
	for _, uuid := range order {
		d := byUUID[uuid]
		if d.Latest.FBUsedMiB+d.Latest.FBFreeMiB > 0 {
			d.TotalMemoryMiB = d.Latest.FBUsedMiB + d.Latest.FBFreeMiB
		}
		d.StalenessSeconds = now.Sub(d.LastSampleAt).Seconds()
		d.IsStale = d.StalenessSeconds > th.MaxStalenessSeconds
		d.Samples = []domain.GPUMetricPoint{d.Latest}
		out = append(out, *d)
	}
	return out, exported
}

func firstLabel(labels map[string]string, candidates []string) string {
	for _, k := range candidates {
		if v, ok := labels[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
