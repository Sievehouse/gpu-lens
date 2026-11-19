// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/Sievehouse/gpu-lens/internal/audit"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// AuthConfig enables HTTP basic authentication for the whole console and API. The admin account may change live
// thresholds, connections and collection; the optional viewer account may only watch.
//
// Basic auth sends the password with every request, so put GPU Lens behind TLS (a reverse proxy or ingress) when it
// is reachable from other machines. See docs/AUTH.md.
type AuthConfig struct {
	AdminUser  string
	AdminPass  string
	ViewerUser string
	ViewerPass string
}

// Enabled reports whether authentication is required.
func (a AuthConfig) Enabled() bool { return a.AdminUser != "" && a.AdminPass != "" }

type principal struct {
	user string
	role string
}

type ctxKey struct{}

func principalOf(r *http.Request) principal {
	if p, ok := r.Context().Value(ctxKey{}).(principal); ok {
		return p
	}
	return principal{user: "anonymous", role: RoleAdmin}
}

// credentialMatches compares in constant time (over fixed-length digests, so lengths do not leak either).
func credentialMatches(gotUser, gotPass, wantUser, wantPass string) bool {
	if wantUser == "" || wantPass == "" {
		return false
	}
	gu, wu := sha256.Sum256([]byte(gotUser)), sha256.Sum256([]byte(wantUser))
	gp, wp := sha256.Sum256([]byte(gotPass)), sha256.Sum256([]byte(wantPass))
	userOK := subtle.ConstantTimeCompare(gu[:], wu[:]) == 1
	passOK := subtle.ConstantTimeCompare(gp[:], wp[:]) == 1
	return userOK && passOK
}

// isPublicPath lists the endpoints that stay reachable without credentials (liveness/readiness probes).
func isPublicPath(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

// authenticate resolves the caller's role. It writes a 401 and returns false when credentials are required and
// missing or wrong.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if !s.auth.Enabled() {
		return r, true
	}
	if isPublicPath(r.URL.Path) {
		return r, true
	}
	user, pass, ok := r.BasicAuth()
	var p principal
	switch {
	case ok && credentialMatches(user, pass, s.auth.AdminUser, s.auth.AdminPass):
		p = principal{user: user, role: RoleAdmin}
	case ok && credentialMatches(user, pass, s.auth.ViewerUser, s.auth.ViewerPass):
		p = principal{user: user, role: RoleViewer}
	default:
		w.Header().Set("WWW-Authenticate", `Basic realm="GPU Lens", charset="UTF-8"`)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)), true
}

func remoteLabel(r *http.Request) string {
	host := hostOnly(r.RemoteAddr)
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if net.ParseIP(first) != nil {
			return host + " (X-Forwarded-For " + first + ")"
		}
	}
	return host
}

// record writes one audit entry for the request's caller.
func (s *Server) record(r *http.Request, action, detail, outcome string) audit.Entry {
	p := principalOf(r)
	return s.audit.Record(audit.Entry{Actor: p.user, Role: p.role, Remote: remoteLabel(r), Action: action, Detail: detail, Outcome: outcome})
}

// requireAdmin refuses viewers. The refusal is audited.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request, action string) bool {
	if principalOf(r).role == RoleAdmin {
		return true
	}
	s.record(r, action, "refused: the viewer role is read-only", audit.OutcomeDenied)
	writeError(w, http.StatusForbidden, "your account has the viewer role, which is read-only: ask an admin to make this change")
	return false
}
