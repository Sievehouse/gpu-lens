package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gpulens/gpu-lens/internal/domain"
)

// GPUResourceName is the extended resource advertised by the NVIDIA Kubernetes device plugin.
const GPUResourceName = "nvidia.com/gpu"

const (
	k8sPageLimit = 500
	k8sMaxPages  = 40
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

// Endpoint returns the configured API server base URL.
func (ks *KubernetesSource) Endpoint() string { return ks.apiServer }

// Guard exposes the read-only transport guard for auditing.
func (ks *KubernetesSource) Guard() *ReadOnlyRoundTripper { return ks.guard }

// SetBearerToken replaces the bearer token used for subsequent requests (projected service-account tokens rotate).
func (ks *KubernetesSource) SetBearerToken(token string) { ks.bearerToken = token }

// RecordedRequests returns the list of HTTP method + path invocations for security auditing in tests.
func (ks *KubernetesSource) RecordedRequests() []string {
	return ks.guard.Calls()
}

// HTTPStatusError is returned when the API server answers with a non-200 status.
type HTTPStatusError struct {
	Path   string
	Status int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("GET %s returned HTTP %d", e.Path, e.Status)
}

// Collect fetches Nodes, Pods, and Events using strictly read-only GET requests.
// Any failure aborts the collection; CollectLive tolerates partial access instead.
func (ks *KubernetesSource) Collect(ctx context.Context) (*CollectorResult, error) {
	now := time.Now().UTC()
	nodes, _, _, err := ks.fetchNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch nodes: %w", err)
	}
	pods, _, _, err := ks.fetchPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch pods: %w", err)
	}
	events, _, err := ks.fetchEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("k8s read-only adapter: fetch events: %w", err)
	}
	ApplyNodeAllocations(nodes, pods)

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

// ApplyNodeAllocations sets each node's allocated GPU count from the GPU requests of pods bound to it that
// have not finished. A bound pod reserves its GPUs as soon as the scheduler places it, before it is Running.
func ApplyNodeAllocations(nodes []domain.NodeState, pods []domain.WorkloadPod) {
	allocatedByNode := make(map[string]int)
	for _, p := range pods {
		if p.NodeName == "" || p.RequestedGPUs <= 0 {
			continue
		}
		if p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		allocatedByNode[p.NodeName] += p.RequestedGPUs
	}
	for i := range nodes {
		nodes[i].GPUAllocated = allocatedByNode[nodes[i].Name]
	}
}

// K8sResourceAccess records whether one read-only list call succeeded.
type K8sResourceAccess struct {
	Resource  string
	OK        bool
	Status    int
	Error     string
	Items     int
	Truncated bool
}

// K8sLiveResult is the outcome of one live collection from the Kubernetes API.
type K8sLiveResult struct {
	Version        string
	Platform       string
	Emulated       bool
	Nodes          []domain.NodeState
	Pods           []domain.WorkloadPod
	Events         []domain.K8sEvent
	Access         []K8sResourceAccess
	TotalNodes     int
	GPUNodes       int
	TotalPodsSeen  int
	OtherResources []string
}

// Usable reports whether at least one inventory list succeeded.
func (r *K8sLiveResult) Usable() bool {
	for _, a := range r.Access {
		if a.OK && (a.Resource == "nodes" || a.Resource == "pods") {
			return true
		}
	}
	return false
}

// CollectLive reads version, nodes, pods and warning events. Each list is independent: a 403 on one resource
// is recorded in Access and does not hide the others. An error is returned only when the API server cannot be
// reached at all.
func (ks *KubernetesSource) CollectLive(ctx context.Context) (*K8sLiveResult, error) {
	res := &K8sLiveResult{}

	var ver struct {
		GitVersion string `json:"gitVersion"`
		Platform   string `json:"platform"`
	}
	verErr := ks.doGetJSON(ctx, "/version", &ver)
	if verErr == nil {
		res.Version = ver.GitVersion
		res.Platform = ver.Platform
		res.Emulated = strings.Contains(ver.GitVersion, "gpulens-lab")
	}

	nodes, otherResources, nodeAccess, nodeErr := ks.fetchNodes(ctx)
	res.Access = append(res.Access, nodeAccess)
	if nodeErr == nil {
		res.Nodes = nodes
		res.TotalNodes = nodeAccess.Items
		res.OtherResources = otherResources
		for _, n := range nodes {
			if n.GPUCapacity > 0 {
				res.GPUNodes++
			}
		}
	}

	pods, podAccess, totalPods, podErr := ks.fetchPods(ctx)
	res.Access = append(res.Access, podAccess)
	if podErr == nil {
		res.Pods = pods
		res.TotalPodsSeen = totalPods
	}

	events, evAccess, evErr := ks.fetchEvents(ctx)
	res.Access = append(res.Access, evAccess)
	if evErr == nil {
		res.Events = events
	}

	if nodeErr == nil && podErr == nil {
		ApplyNodeAllocations(res.Nodes, res.Pods)
	}

	// Unreachable: nothing answered at all (connection refused, TLS failure, timeout ...).
	if verErr != nil && nodeErr != nil && podErr != nil && evErr != nil {
		var statusErr *HTTPStatusError
		if errors.As(nodeErr, &statusErr) || errors.As(verErr, &statusErr) {
			// The server answered (for example 401/403 everywhere): reachable but not usable.
			return res, nil
		}
		return nil, nodeErr
	}
	return res, nil
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
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		pathOnly := path
		if i := strings.IndexByte(pathOnly, '?'); i >= 0 {
			pathOnly = pathOnly[:i]
		}
		return &HTTPStatusError{Path: pathOnly, Status: resp.StatusCode}
	}
	limited := io.LimitReader(resp.Body, ks.maxResponseBytes)
	return json.NewDecoder(limited).Decode(dest)
}

type k8sListMeta struct {
	Continue string `json:"continue"`
}

// listPaged walks a list endpoint with limit/continue pagination. `decode` receives each page.
func (ks *KubernetesSource) listPaged(ctx context.Context, resource, path string, baseQuery url.Values, newPage func() interface{}, onPage func(page interface{}) (items int, cont string)) (K8sResourceAccess, error) {
	access := K8sResourceAccess{Resource: resource}
	cont := ""
	for page := 0; page < k8sMaxPages; page++ {
		q := url.Values{}
		for k, v := range baseQuery {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(k8sPageLimit))
		if cont != "" {
			q.Set("continue", cont)
		}
		dest := newPage()
		if err := ks.doGetJSON(ctx, path+"?"+q.Encode(), dest); err != nil {
			var statusErr *HTTPStatusError
			if errors.As(err, &statusErr) {
				access.Status = statusErr.Status
			}
			access.Error = err.Error()
			return access, err
		}
		n, next := onPage(dest)
		access.Items += n
		cont = next
		if cont == "" {
			access.OK = true
			access.Status = http.StatusOK
			return access, nil
		}
	}
	access.OK = true
	access.Status = http.StatusOK
	access.Truncated = true
	return access, nil
}

// Minimal Kubernetes wire structs for safe, sanitized unmarshaling (ignoring env/command/args/secrets).
type k8sNodeList struct {
	Metadata k8sListMeta   `json:"metadata"`
	Items    []k8sNodeItem `json:"items"`
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

func parseQuantityInt(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (ks *KubernetesSource) fetchNodes(ctx context.Context) ([]domain.NodeState, []string, K8sResourceAccess, error) {
	var out []domain.NodeState
	other := map[string]bool{}
	access, err := ks.listPaged(ctx, "nodes", "/api/v1/nodes", nil,
		func() interface{} { return &k8sNodeList{} },
		func(page interface{}) (int, string) {
			raw := page.(*k8sNodeList)
			for _, item := range raw.Items {
				out = append(out, normalizeNode(item))
				// Other NVIDIA extended resources (MIG slices, shared GPUs) are reported but not modeled.
				for res := range item.Status.Capacity {
					if strings.HasPrefix(res, "nvidia.com/") && res != GPUResourceName {
						other[res] = true
					}
				}
			}
			return len(raw.Items), raw.Metadata.Continue
		})
	if err != nil {
		return nil, nil, access, err
	}
	var otherList []string
	for res := range other {
		otherList = append(otherList, res)
	}
	sort.Strings(otherList)
	return out, otherList, access, nil
}

func normalizeNode(item k8sNodeItem) domain.NodeState {
	capGPUs := parseQuantityInt(item.Status.Capacity[GPUResourceName])
	allocGPUs := parseQuantityInt(item.Status.Allocatable[GPUResourceName])
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
	return domain.NodeState{
		Name:           item.Metadata.Name,
		Ready:          ready,
		Schedulable:    !item.Spec.Unschedulable,
		GPUModel:       model,
		GPUCapacity:    capGPUs,
		GPUAllocatable: allocGPUs,
		Labels:         item.Metadata.Labels,
		Taints:         taints,
		Conditions:     conds,
	}
}

type k8sPodList struct {
	Metadata k8sListMeta  `json:"metadata"`
	Items    []k8sPodItem `json:"items"`
}

type k8sContainerSpec struct {
	Name      string `json:"name"`
	Resources struct {
		Limits   map[string]string `json:"limits"`
		Requests map[string]string `json:"requests"`
	} `json:"resources"`
}

type k8sTerminated struct {
	ExitCode   int       `json:"exitCode"`
	Reason     string    `json:"reason"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
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
		NodeName       string             `json:"nodeName"`
		NodeSelector   map[string]string  `json:"nodeSelector"`
		Containers     []k8sContainerSpec `json:"containers"`
		InitContainers []k8sContainerSpec `json:"initContainers"`
		Tolerations    []struct {
			Key      string `json:"key"`
			Operator string `json:"operator"`
			Value    string `json:"value"`
			Effect   string `json:"effect"`
		} `json:"tolerations"`
	} `json:"spec"`
	Status struct {
		Phase      string     `json:"phase"`
		Reason     string     `json:"reason"`
		StartTime  *time.Time `json:"startTime"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
				Terminated *k8sTerminated `json:"terminated"`
			} `json:"state"`
			LastState struct {
				Terminated *k8sTerminated `json:"terminated"`
			} `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func containerGPUs(c k8sContainerSpec) int {
	if v, ok := c.Resources.Limits[GPUResourceName]; ok {
		return parseQuantityInt(v)
	}
	if v, ok := c.Resources.Requests[GPUResourceName]; ok {
		return parseQuantityInt(v)
	}
	return 0
}

func (ks *KubernetesSource) fetchPods(ctx context.Context) ([]domain.WorkloadPod, K8sResourceAccess, int, error) {
	var out []domain.WorkloadPod
	access, err := ks.listPaged(ctx, "pods", "/api/v1/pods", nil,
		func() interface{} { return &k8sPodList{} },
		func(page interface{}) (int, string) {
			raw := page.(*k8sPodList)
			for _, item := range raw.Items {
				if pod, ok := normalizePod(item); ok {
					out = append(out, pod)
				}
			}
			return len(raw.Items), raw.Metadata.Continue
		})
	if err != nil {
		return nil, access, 0, err
	}
	return out, access, access.Items, nil
}

// normalizePod keeps only pods that request GPUs and only non-sensitive fields.
func normalizePod(item k8sPodItem) (domain.WorkloadPod, bool) {
	reqGPUs := 0
	for _, c := range item.Spec.Containers {
		reqGPUs += containerGPUs(c)
	}
	// The effective request is the larger of the app containers' sum and any single init container.
	for _, c := range item.Spec.InitContainers {
		if n := containerGPUs(c); n > reqGPUs {
			reqGPUs = n
		}
	}
	if reqGPUs <= 0 {
		return domain.WorkloadPod{}, false
	}
	ownerKind := "Pod"
	ownerName := item.Metadata.Name
	if len(item.Metadata.OwnerReferences) > 0 {
		ownerKind = item.Metadata.OwnerReferences[0].Kind
		ownerName = item.Metadata.OwnerReferences[0].Name
	}
	restarts := 0
	var lastTerm *domain.ContainerTermination
	statusDetail := ""
	for _, cs := range item.Status.ContainerStatuses {
		restarts += cs.RestartCount
		term := cs.LastState.Terminated
		if term == nil && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			term = cs.State.Terminated
		}
		if term != nil && (lastTerm == nil || term.FinishedAt.After(lastTerm.FinishedAt)) {
			lastTerm = &domain.ContainerTermination{
				ExitCode:   term.ExitCode,
				Reason:     term.Reason,
				Message:    term.Message,
				StartedAt:  term.StartedAt,
				FinishedAt: term.FinishedAt,
			}
		}
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && statusDetail == "" {
			statusDetail = cs.State.Waiting.Reason
		}
	}

	phase := item.Status.Phase
	schedReason, schedMsg := "", ""
	if phase == "Pending" {
		if item.Spec.NodeName != "" {
			// Already placed by the scheduler: waiting on image pull / container start, not on GPU capacity.
			phase = "ContainerCreating"
			if statusDetail == "" {
				statusDetail = "ContainerCreating"
			}
		} else {
			for _, c := range item.Status.Conditions {
				if c.Type == "PodScheduled" && c.Status == "False" {
					schedReason, schedMsg = c.Reason, c.Message
					if schedReason == "Unschedulable" {
						schedReason = "FailedScheduling"
					}
				}
			}
		}
	}
	if statusDetail == "" && item.Status.Reason != "" {
		statusDetail = item.Status.Reason
	}

	var tolerations []string
	for _, t := range item.Spec.Tolerations {
		if strings.HasPrefix(t.Key, "node.kubernetes.io/") {
			continue
		}
		op := t.Operator
		if op == "" {
			op = "Equal"
		}
		tolerations = append(tolerations, strings.TrimSuffix(fmt.Sprintf("%s %s %s:%s", t.Key, op, t.Value, t.Effect), ":"))
	}

	return domain.WorkloadPod{
		Name:             item.Metadata.Name,
		Namespace:        item.Metadata.Namespace,
		UID:              item.Metadata.UID,
		OwnerKind:        ownerKind,
		OwnerName:        ownerName,
		Phase:            phase,
		NodeName:         item.Spec.NodeName,
		RequestedGPUs:    reqGPUs,
		CreatedAt:        item.Metadata.CreationTimestamp,
		StartedAt:        item.Status.StartTime,
		RestartCount:     restarts,
		LastTermination:  lastTerm,
		NodeSelector:     item.Spec.NodeSelector,
		Tolerations:      tolerations,
		SchedulerReason:  schedReason,
		SchedulerMessage: schedMsg,
		StatusDetail:     statusDetail,
	}, true
}

type k8sEventItem struct {
	Metadata struct {
		UID               string    `json:"uid"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Type           string     `json:"type"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Count          int        `json:"count"`
	FirstTimestamp time.Time  `json:"firstTimestamp"`
	LastTimestamp  time.Time  `json:"lastTimestamp"`
	EventTime      *time.Time `json:"eventTime"`
	Series         *struct {
		Count            int        `json:"count"`
		LastObservedTime *time.Time `json:"lastObservedTime"`
	} `json:"series"`
	Source struct {
		Component string `json:"component"`
		Host      string `json:"host"`
	} `json:"source"`
	ReportingComponent string `json:"reportingComponent"`
	InvolvedObject     struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"involvedObject"`
}

type k8sEventList struct {
	Metadata k8sListMeta    `json:"metadata"`
	Items    []k8sEventItem `json:"items"`
}

func (ks *KubernetesSource) fetchEvents(ctx context.Context) ([]domain.K8sEvent, K8sResourceAccess, error) {
	var out []domain.K8sEvent
	// Warning events carry every signal the rules use (FailedScheduling, BackOff, node problem reports).
	access, err := ks.listPaged(ctx, "events", "/api/v1/events", url.Values{"fieldSelector": {"type=Warning"}},
		func() interface{} { return &k8sEventList{} },
		func(page interface{}) (int, string) {
			raw := page.(*k8sEventList)
			for _, ev := range raw.Items {
				out = append(out, normalizeEvent(ev))
			}
			return len(raw.Items), raw.Metadata.Continue
		})
	if err != nil {
		return nil, access, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastTimestamp.Before(out[j].LastTimestamp) })
	return out, access, nil
}

func normalizeEvent(ev k8sEventItem) domain.K8sEvent {
	count := ev.Count
	if ev.Series != nil && ev.Series.Count > count {
		count = ev.Series.Count
	}
	if count <= 0 {
		count = 1
	}
	// Events written through events.k8s.io leave lastTimestamp empty and use eventTime / series instead.
	last := ev.LastTimestamp
	if last.IsZero() && ev.Series != nil && ev.Series.LastObservedTime != nil {
		last = *ev.Series.LastObservedTime
	}
	if last.IsZero() && ev.EventTime != nil {
		last = *ev.EventTime
	}
	if last.IsZero() {
		last = ev.FirstTimestamp
	}
	if last.IsZero() {
		last = ev.Metadata.CreationTimestamp
	}
	first := ev.FirstTimestamp
	if first.IsZero() && ev.EventTime != nil {
		first = *ev.EventTime
	}
	if first.IsZero() {
		first = last
	}
	component := ev.Source.Component
	if component == "" {
		component = ev.ReportingComponent
	}
	nodeName := ev.Source.Host
	if nodeName == "" && ev.InvolvedObject.Kind == "Node" {
		nodeName = ev.InvolvedObject.Name
	}
	return domain.K8sEvent{
		ID:                      ev.Metadata.UID,
		Type:                    ev.Type,
		Reason:                  ev.Reason,
		Message:                 ev.Message,
		SourceComponent:         component,
		InvolvedObjectKind:      ev.InvolvedObject.Kind,
		InvolvedObjectNamespace: ev.InvolvedObject.Namespace,
		InvolvedObjectName:      ev.InvolvedObject.Name,
		NodeName:                nodeName,
		Count:                   count,
		FirstTimestamp:          first,
		LastTimestamp:           last,
	}
}
