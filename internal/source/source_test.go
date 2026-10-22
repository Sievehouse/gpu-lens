package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gpulens/gpu-lens/internal/config"
)

func TestReadOnlyRoundTripperBlocksMutations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	guard := NewReadOnlyRoundTripper(http.DefaultTransport)
	client := &http.Client{Transport: guard}

	mutatingMethods := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	for _, method := range mutatingMethods {
		req, err := http.NewRequest(method, srv.URL+"/api/v1/pods/default/train-job", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		_, err = client.Do(req)
		if !errors.Is(err, ErrMutatingMethodForbidden) {
			t.Fatalf("expected ErrMutatingMethodForbidden for %s, got %v", method, err)
		}
	}

	// Verify GET is permitted
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/pods", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected GET to succeed, got %v", err)
	}
	resp.Body.Close()
}

func TestDCGMExpositionParserAndUnsupportedMetricSafeguard(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	sampleExposition := `# HELP DCGM_FI_DEV_GPU_UTIL GPU utilization (in %).
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-aaaa-1111",pci_bus_id="00000000:17:00.0",modelName="NVIDIA H100 80GB HBM3",Hostname="gpu-node-01",pod="llama3-train-0",namespace="ml-train",container="trainer"} 84.5 1792324790000
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature (in C).
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-aaaa-1111",Hostname="gpu-node-01",pod="llama3-train-0",namespace="ml-train"} 67.0 1792324790000
# HELP DCGM_FI_DEV_FB_USED Framebuffer memory used (in MiB).
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-aaaa-1111",Hostname="gpu-node-01",pod="llama3-train-0",namespace="ml-train"} 65536 1792324790000
# HELP DCGM_FI_DEV_FB_FREE Framebuffer memory free (in MiB).
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-aaaa-1111",Hostname="gpu-node-01",pod="llama3-train-0",namespace="ml-train"} 16384 1792324790000
`
	series, err := ParseDCGMExposition(sampleExposition, now)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(series) != 4 {
		t.Fatalf("expected 4 series, got %d", len(series))
	}

	mapping := config.DefaultMetricMapping()
	th := config.DefaultRuleThresholds()
	gpus, exported := NormalizeSeriesToGPUs(series, mapping, th, now)
	if len(gpus) != 1 {
		t.Fatalf("expected 1 normalized GPU, got %d", len(gpus))
	}
	g := gpus[0]
	if g.UUID != "GPU-aaaa-1111" || g.AllocatedPod != "llama3-train-0" || g.AllocatedNamespace != "ml-train" {
		t.Fatalf("unexpected GPU metadata: %+v", g)
	}
	// Verify optional metrics that were NOT in the exposition are nil, never fabricated!
	if g.Latest.ECCDBEVolTotal != nil {
		t.Fatalf("expected unexported optional metric ECCDBEVolTotal to be nil, got %v", *g.Latest.ECCDBEVolTotal)
	}
	if g.Latest.NVLinkCRCFlitErrors != nil {
		t.Fatalf("expected unexported optional metric NVLinkCRCFlitErrors to be nil, got %v", *g.Latest.NVLinkCRCFlitErrors)
	}
	if exported["DCGM_FI_DEV_ECC_DBE_VOL_TOTAL"] {
		t.Fatalf("expected unexported metric to be false in exported map")
	}
}

func TestKubernetesSourceReadOnlyEnforcement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("KubernetesSource issued non-GET method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes":
			w.Write([]byte(`{"items":[{"metadata":{"name":"gpu-node-01","labels":{"nvidia.com/gpu.product":"NVIDIA-H100"}},"spec":{"unschedulable":false},"status":{"capacity":{"nvidia.com/gpu":"8"},"allocatable":{"nvidia.com/gpu":"8"},"conditions":[{"type":"Ready","status":"True"}]}}]}`))
		case "/api/v1/pods":
			w.Write([]byte(`{"items":[{"metadata":{"name":"job-1","namespace":"ml","uid":"u1"},"spec":{"nodeName":"gpu-node-01","containers":[{"name":"main","resources":{"limits":{"nvidia.com/gpu":"4"}}}]},"status":{"phase":"Running"}}]}`))
		case "/api/v1/events":
			w.Write([]byte(`{"items":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ks := NewKubernetesSource(srv.URL, "dummy-token", 5*time.Second, 1024*1024, http.DefaultTransport)
	res, err := ks.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}
	if len(res.Snapshot.Nodes) != 1 || res.Snapshot.Nodes[0].GPUAllocated != 4 {
		t.Fatalf("unexpected node state: %+v", res.Snapshot.Nodes)
	}
	for _, call := range ks.RecordedRequests() {
		if len(call) < 3 || call[:3] != "GET" {
			t.Fatalf("expected only GET calls, saw %q", call)
		}
	}
}
