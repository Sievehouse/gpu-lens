// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package source

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
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

// maxRecordedCalls bounds the audit trail kept by ReadOnlyRoundTripper so that a long-running live
// collector cannot grow memory without limit.
const maxRecordedCalls = 256

// ReadOnlyRoundTripper wraps an http.RoundTripper and strictly blocks any mutating HTTP verb
// (POST, PUT, PATCH, DELETE, CONNECT, TRACE) at the transport boundary.
type ReadOnlyRoundTripper struct {
	Base           http.RoundTripper
	AllowedMethods map[string]bool
	// RecordedCalls holds the most recent "METHOD path" pairs seen by the guard (bounded audit trail).
	RecordedCalls []string

	mu      sync.Mutex
	allowed int64
	blocked int64
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
	permitted := rt.AllowedMethods[req.Method]
	rt.mu.Lock()
	rt.RecordedCalls = append(rt.RecordedCalls, fmt.Sprintf("%s %s", req.Method, req.URL.Path))
	if len(rt.RecordedCalls) > maxRecordedCalls {
		rt.RecordedCalls = append([]string(nil), rt.RecordedCalls[len(rt.RecordedCalls)-maxRecordedCalls:]...)
	}
	if permitted {
		rt.allowed++
	} else {
		rt.blocked++
	}
	rt.mu.Unlock()
	if !permitted {
		return nil, fmt.Errorf("%w: blocked method %q on %q", ErrMutatingMethodForbidden, req.Method, req.URL.String())
	}
	return rt.Base.RoundTrip(req)
}

// Calls returns a copy of the bounded audit trail.
func (rt *ReadOnlyRoundTripper) Calls() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.RecordedCalls...)
}

// Stats returns how many requests the guard has allowed and blocked since it was created.
func (rt *ReadOnlyRoundTripper) Stats() (allowed, blocked int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.allowed, rt.blocked
}

// GuardSelfTestResult reports the outcome of SelfTestReadOnlyGuard.
type GuardSelfTestResult struct {
	BlockedMethods []string
	LeakedMethods  []string
	NetworkCalls   int
}

// OK reports whether every mutating verb was rejected without any network I/O.
func (r GuardSelfTestResult) OK() bool {
	return len(r.LeakedMethods) == 0 && r.NetworkCalls == 0 && len(r.BlockedMethods) > 0
}

type countingTransport struct {
	mu    sync.Mutex
	calls int
}

func (c *countingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return nil, errors.New("self-test transport: network I/O is disabled")
}

// SelfTestReadOnlyGuard proves, in process and without touching the network, that the read-only guard rejects
// every mutating HTTP verb before the request can reach the underlying transport.
func SelfTestReadOnlyGuard() GuardSelfTestResult {
	inner := &countingTransport{}
	guard := NewReadOnlyRoundTripper(inner)
	client := &http.Client{Transport: guard, Timeout: 2 * time.Second}
	res := GuardSelfTestResult{}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, err := http.NewRequest(method, "http://read-only-guard.selftest.invalid/api/v1/nodes", nil)
		if err != nil {
			res.LeakedMethods = append(res.LeakedMethods, method)
			continue
		}
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if errors.Is(err, ErrMutatingMethodForbidden) {
			res.BlockedMethods = append(res.BlockedMethods, method)
		} else {
			res.LeakedMethods = append(res.LeakedMethods, method)
		}
	}
	inner.mu.Lock()
	res.NetworkCalls = inner.calls
	inner.mu.Unlock()
	return res
}

// NewBaseTransport builds the HTTP transport used underneath the read-only guard. When caFile is set, the
// certificates in that PEM bundle are trusted in addition to the system roots (used for the Kubernetes API
// server CA). TLS verification is never disabled.
func NewBaseTransport(caFile string) (http.RoundTripper, error) {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA bundle %q: %w", caFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %q contains no usable PEM certificates", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return tr, nil
}
