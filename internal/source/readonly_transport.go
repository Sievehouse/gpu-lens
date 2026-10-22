package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrMutatingMethodForbidden is returned whenever a caller attempts a non-read-only HTTP method.
var ErrMutatingMethodForbidden = errors.New("security boundary violation: GPU Lens external adapters permit only GET and HEAD requests")

// TelemetryCollector is the normalized source interface consumed by the application.
type TelemetryCollector interface {
	ID() string
	Name() string
	Collect(ctx context.Context) (*CollectorResult, error)
}

// CollectorResult wraps a partial or full ClusterSnapshot returned by a source adapter.
type CollectorResult struct {
	Snapshot *ClusterSnapshotAlias
}

// ReadOnlyRoundTripper wraps an http.RoundTripper and strictly blocks any mutating HTTP verb
// (POST, PUT, PATCH, DELETE, CONNECT, TRACE) at the transport boundary.
type ReadOnlyRoundTripper struct {
	Base           http.RoundTripper
	AllowedMethods map[string]bool
	RecordedCalls  []string
}

// NewReadOnlyRoundTripper creates a transport guard allowing only GET and HEAD.
func NewReadOnlyRoundTripper(base http.RoundTripper) *ReadOnlyRoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &ReadOnlyRoundTripper{
		Base: base,
		AllowedMethods: map[string]bool{
			http.MethodGet:  true,
			http.MethodHead: true,
		},
	}
}

// RoundTrip enforces the read-only HTTP method invariant before delegating to Base.
func (rt *ReadOnlyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("nil http request")
	}
	rt.RecordedCalls = append(rt.RecordedCalls, fmt.Sprintf("%s %s", req.Method, req.URL.Path))
	if !rt.AllowedMethods[req.Method] {
		return nil, fmt.Errorf("%w: blocked method %q on %q", ErrMutatingMethodForbidden, req.Method, req.URL.String())
	}
	return rt.Base.RoundTrip(req)
}
