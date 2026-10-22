package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/domain"
)

// KubernetesSource is a strictly read-only adapter for inspecting Kubernetes Nodes, Pods, and Events.
// It uses ReadOnlyRoundTripper to guarantee that no mutating HTTP request (POST, PUT, PATCH, DELETE)
// can ever be sent to the Kubernetes API server.
type KubernetesSource struct {
	apiServer        string
	bearerToken      string
	maxResponseBytes int64
	client           *http.Client
	guard            *ReadOnlyRoundTripper
}

// NewKubernetesSource constructs a read-only Kubernetes API adapter.
func NewKubernetesSource(apiServer, bearerToken string, timeout time.Duration, maxBytes int64, baseTransport http.RoundTripper) *KubernetesSource {
	guard := NewReadOnlyRoundTripper(baseTransport)
	if maxBytes <= 0 {
		maxBytes = 16 * 1024 * 1024
	}
	return &KubernetesSource{
		apiServer:        strings.TrimRight(apiServer, "/"),
		bearerToken:      bearerToken,
		maxResponseBytes: maxBytes,
		guard:            guard,
		client: &http.Client{
			Timeout:   timeout,
			Transport: guard,
		},
	}
}

func (ks *KubernetesSource) ID() string   { return "kubernetes-readonly" }
func (ks *KubernetesSource) Name() string { return "Kubernetes API (Read-Only Adapter)" }

// RecordedRequests returns the list of HTTP method + path invocations for security auditing in tests.
func (ks *KubernetesSource) RecordedRequests() []string {
	return ks.guard.RecordedCalls
}

// Collect fetches Nodes, Pods, and Events using strictly read-only GET requests.
func (ks *KubernetesSource) Collect(ctx context.Context) (*CollectorResult, error) {
	now := time.Now().UTC()
	nodes, err := ks.fetchNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch nodes: %w", err)
	}
	pods, err := ks.fetchPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch pods: %w", err)
	}
	events, err := ks.fetchEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch events: %w", err)
	}

	// Calculate per-node allocated GPUs from Running pods
	allocatedByNode := make(map[string]int)
	for _, p := range pods {
		if p.Phase == "Running" && p.NodeName != "" && p.RequestedGPUs > 0 {
			allocatedByNode[p.NodeName] += p.RequestedGPUs
		}
	}
	for i := range nodes {
		nodes[i].GPUAllocated = allocatedByNode[nodes[i].Name]
	}

	snap := &domain.ClusterSnapshot{
		ClusterName: "live-kubernetes-cluster",
		DataMode:    "live_telemetry",
		CollectedAt: now,
		Nodes:       nodes,
		Pods:        pods,
		Events:      events,
		Sources: []domain.SourceHealth{
			{
				ID:               ks.ID(),
				Name:             ks.Name(),
				Kind:             "kubernetes",
				Mode:             "live",
				Endpoint:         ks.apiServer,
				Available:        true,
				ReadOnlyVerified: true,
				LastSuccessAt:    now,
				StalenessSeconds: 0,
				IsStale:          false,
				Notes:            "Strictly read-only GET access to /api/v1/nodes, /api/v1/pods, /api/v1/events",
			},
		},
	}
	return &CollectorResult{Snapshot: snap}, nil
}

func (ks *KubernetesSource) doGetJSON(ctx context.Context, path string, dest interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.apiServer+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if ks.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+ks.bearerToken)
	}
	resp, err := ks.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned HTTP %d", path, resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, ks.maxResponseBytes)
	return json.NewDecoder(limited).Decode(dest)
}

// Minimal Kubernetes wire structs for safe, sanitized unmarshaling (ignoring env/command/args/secrets).
type k8sNodeList struct {
	Items []k8sNodeItem `json:"items"`
}

type k8sNodeItem struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
		Taints        []struct {
			Key    string `json:"key"`
			Value  string `json:"value"`
			Effect string `json:"effect"`
		} `json:"taints"`
	} `json:"spec"`
	Status struct {
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
		Conditions  []struct {
			Type               string    `json:"type"`
			Status             string    `json:"status"`
			Reason             string    `json:"reason"`
			Message            string    `json:"message"`
			LastTransitionTime time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
	} `json:"status"`
}

func (ks *KubernetesSource) fetchNodes(ctx context.Context) ([]domain.NodeState, error) {
	var raw k8sNodeList
	if err := ks.doGetJSON(ctx, "/api/v1/nodes", &raw); err != nil {
		return nil, err
	}
	var out []domain.NodeState
	for _, item := range raw.Items {
		capGPUs, _ := strconv.Atoi(item.Status.Capacity["nvidia.com/gpu"])
		allocGPUs, _ := strconv.Atoi(item.Status.Allocatable["nvidia.com/gpu"])
		ready := false
		var conds []domain.NodeCondition
		for _, c := range item.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
			conds = append(conds, domain.NodeCondition{
				Type:               c.Type,
				Status:             c.Status,
				Reason:             c.Reason,
				Message:            c.Message,
				LastTransitionTime: c.LastTransitionTime,
			})
		}
		var taints []string
		for _, t := range item.Spec.Taints {
			taints = append(taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
		}
		model := item.Metadata.Labels["nvidia.com/gpu.product"]
		if model == "" {
			model = "NVIDIA Datacenter GPU"
		}
		out = append(out, domain.NodeState{
			Name:           item.Metadata.Name,
			Ready:          ready,
			Schedulable:    !item.Spec.Unschedulable,
			GPUModel:       model,
			GPUCapacity:    capGPUs,
			GPUAllocatable: allocGPUs,
			Labels:         item.Metadata.Labels,
			Taints:         taints,
			Conditions:     conds,
		})
	}
	return out, nil
}

type k8sPodList struct {
	Items []k8sPodItem `json:"items"`
}

type k8sPodItem struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		UID               string    `json:"uid"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
		OwnerReferences   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		NodeName     string            `json:"nodeName"`
		NodeSelector map[string]string `json:"nodeSelector"`
		Containers   []struct {
			Name      string `json:"name"`
			Resources struct {
				Limits   map[string]string `json:"limits"`
				Requests map[string]string `json:"requests"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string     `json:"phase"`
		StartTime         *time.Time `json:"startTime"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			RestartCount int    `json:"restartCount"`
			LastState    struct {
				Terminated *struct {
					ExitCode   int       `json:"exitCode"`
					Reason     string    `json:"reason"`
					Message    string    `json:"message"`
					StartedAt  time.Time `json:"startedAt"`
					FinishedAt time.Time `json:"finishedAt"`
				} `json:"terminated"`
			} `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (ks *KubernetesSource) fetchPods(ctx context.Context) ([]domain.WorkloadPod, error) {
	var raw k8sPodList
	if err := ks.doGetJSON(ctx, "/api/v1/pods", &raw); err != nil {
		return nil, err
	}
	var out []domain.WorkloadPod
	for _, item := range raw.Items {
		reqGPUs := 0
		for _, c := range item.Spec.Containers {
			if v, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
				n, _ := strconv.Atoi(v)
				reqGPUs += n
			} else if v, ok := c.Resources.Requests["nvidia.com/gpu"]; ok {
				n, _ := strconv.Atoi(v)
				reqGPUs += n
			}
		}
		if reqGPUs <= 0 {
			continue
		}
		ownerKind := "Pod"
		ownerName := item.Metadata.Name
		if len(item.Metadata.OwnerReferences) > 0 {
			ownerKind = item.Metadata.OwnerReferences[0].Kind
			ownerName = item.Metadata.OwnerReferences[0].Name
		}
		restarts := 0
		var lastTerm *domain.ContainerTermination
		for _, cs := range item.Status.ContainerStatuses {
			restarts += cs.RestartCount
			if cs.LastState.Terminated != nil {
				t := cs.LastState.Terminated
				lastTerm = &domain.ContainerTermination{
					ExitCode:   t.ExitCode,
					Reason:     t.Reason,
					Message:    t.Message,
					StartedAt:  t.StartedAt,
					FinishedAt: t.FinishedAt,
				}
			}
		}
		out = append(out, domain.WorkloadPod{
			Name:          item.Metadata.Name,
			Namespace:     item.Metadata.Namespace,
			UID:           item.Metadata.UID,
			OwnerKind:     ownerKind,
			OwnerName:     ownerName,
			Phase:         item.Status.Phase,
			NodeName:      item.Spec.NodeName,
			RequestedGPUs: reqGPUs,
			CreatedAt:     item.Metadata.CreationTimestamp,
			StartedAt:     item.Status.StartTime,
			RestartCount:  restarts,
			LastTermination: lastTerm,
			NodeSelector:  item.Spec.NodeSelector,
		})
	}
	return out, nil
}

type k8sEventList struct {
	Items []struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
		Type           string    `json:"type"`
		Reason         string    `json:"reason"`
		Message        string    `json:"message"`
		Count          int       `json:"count"`
		FirstTimestamp time.Time `json:"firstTimestamp"`
		LastTimestamp  time.Time `json:"lastTimestamp"`
		Source         struct {
			Component string `json:"component"`
			Host      string `json:"host"`
		} `json:"source"`
		InvolvedObject struct {
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"involvedObject"`
	} `json:"items"`
}

func (ks *KubernetesSource) fetchEvents(ctx context.Context) ([]domain.K8sEvent, error) {
	var raw k8sEventList
	if err := ks.doGetJSON(ctx, "/api/v1/events", &raw); err != nil {
		return nil, err
	}
	var out []domain.K8sEvent
	for _, ev := range raw.Items {
		count := ev.Count
		if count <= 0 {
			count = 1
		}
		out = append(out, domain.K8sEvent{
			ID:                      ev.Metadata.UID,
			Type:                    ev.Type,
			Reason:                  ev.Reason,
			Message:                 ev.Message,
			SourceComponent:         ev.Source.Component,
			InvolvedObjectKind:      ev.InvolvedObject.Kind,
			InvolvedObjectNamespace: ev.InvolvedObject.Namespace,
			InvolvedObjectName:      ev.InvolvedObject.Name,
			NodeName:                ev.Source.Host,
			Count:                   count,
			FirstTimestamp:          ev.FirstTimestamp,
			LastTimestamp:           ev.LastTimestamp,
		})
	}
	return out, nil
}
