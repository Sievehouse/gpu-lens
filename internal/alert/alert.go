// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

// Package alert delivers incident notifications to Slack and to a generic JSON webhook when a live incident opens,
// resolves or changes severity.
//
// Alerting is the only outbound write GPU Lens can make, and it is off unless an operator configures a destination
// at startup (flag or environment variable). Destinations can never be set or changed over the HTTP API, so a
// browser or API caller cannot turn GPU Lens into a request relay. Deliveries never touch the telemetry sources and
// never go through the read-only transport used for them.
package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/domain"
)

// Event types.
const (
	EventOpened          = "incident.opened"
	EventResolved        = "incident.resolved"
	EventSeverityChanged = "incident.severity_changed"
	EventTest            = "test"

	// Schema identifies the generic webhook payload format.
	Schema = "gpulens.alert/v1"
)

const (
	queueSize       = 256
	maxAttempts     = 3
	deliveryTimeout = 10 * time.Second
	testTimeout     = 5 * time.Second
)

// Config selects where alerts go. Empty URLs mean "not configured".
type Config struct {
	SlackWebhookURL string `json:"-"`
	WebhookURL      string `json:"-"`
	WebhookSecret   string `json:"-"`
	// MinSeverity is the lowest severity that is sent: info, warning (default) or critical.
	MinSeverity string `json:"min_severity"`
	// PublicURL is the externally reachable address of this GPU Lens console, used for "open in GPU Lens" links.
	PublicURL string `json:"public_url"`
}

// Validate checks the destination URLs and the severity floor.
func (c Config) Validate() error {
	if c.SlackWebhookURL != "" {
		if err := checkURL("Slack webhook", c.SlackWebhookURL, true); err != nil {
			return err
		}
	}
	if c.WebhookURL != "" {
		if err := checkURL("alert webhook", c.WebhookURL, false); err != nil {
			return err
		}
	}
	if c.PublicURL != "" {
		if err := checkURL("public URL", c.PublicURL, false); err != nil {
			return err
		}
	}
	if severityRank(normalizeSeverity(c.MinSeverity)) == 0 {
		return fmt.Errorf("alert: min severity must be info, warning or critical, got %q", c.MinSeverity)
	}
	return nil
}

func checkURL(what, raw string, httpsOnly bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("alert: %s is not a valid URL", what)
	}
	if u.Scheme != "https" && (httpsOnly || u.Scheme != "http") {
		if httpsOnly {
			return fmt.Errorf("alert: %s must use https", what)
		}
		return fmt.Errorf("alert: %s must use http or https", what)
	}
	if u.User != nil {
		return fmt.Errorf("alert: %s must not embed credentials; use the signing secret instead", what)
	}
	return nil
}

func normalizeSeverity(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "warning"
	}
	return s
}

func severityRank(s string) int {
	switch s {
	case "info":
		return 1
	case "warning":
		return 2
	case "critical":
		return 3
	}
	return 0
}

// IncidentRef is the part of an incident carried in an alert.
type IncidentRef struct {
	ID                string    `json:"id"`
	Fingerprint       string    `json:"fingerprint"`
	RuleID            string    `json:"rule_id"`
	RuleName          string    `json:"rule_name"`
	Category          string    `json:"category"`
	Severity          string    `json:"severity"`
	EvalStatus        string    `json:"eval_status"`
	EvidenceStrength  string    `json:"evidence_strength"`
	Title             string    `json:"title"`
	Summary           string    `json:"summary"`
	AffectedResources []string  `json:"affected_resources"`
	FirstSeenAt       time.Time `json:"first_seen_at"`
}

// Event is one notification. It is also the exact body POSTed to the generic webhook.
type Event struct {
	Schema           string       `json:"schema"`
	Type             string       `json:"type"`
	DeliveryID       string       `json:"delivery_id"`
	Timestamp        time.Time    `json:"timestamp"`
	Cluster          string       `json:"cluster"`
	DataMode         string       `json:"data_mode"`
	Emulated         bool         `json:"emulated"`
	Incident         *IncidentRef `json:"incident,omitempty"`
	PreviousSeverity string       `json:"previous_severity,omitempty"`
	DurationSeconds  float64      `json:"duration_seconds,omitempty"`
	ConsoleURL       string       `json:"console_url,omitempty"`
	Message          string       `json:"message,omitempty"`
	GPULensVersion   string       `json:"gpulens_version"`
}

// Meta describes where the incidents came from.
type Meta struct {
	Cluster  string
	DataMode string
	Version  string
}

// DestinationStatus reports the delivery record of one destination. The target never includes the secret part of
// the URL (Slack webhook paths are credentials).
type DestinationStatus struct {
	Kind          string     `json:"kind"`
	Target        string     `json:"target"`
	Signed        bool       `json:"signed"`
	Sent          int        `json:"sent"`
	Failed        int        `json:"failed"`
	Dropped       int        `json:"dropped"`
	LastEvent     string     `json:"last_event"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     string     `json:"last_error"`
}

// Status is the alerting section of the live status payload.
type Status struct {
	Enabled          bool                `json:"enabled"`
	MinSeverity      string              `json:"min_severity"`
	PublicURL        string              `json:"public_url"`
	TrackedIncidents int                 `json:"tracked_incidents"`
	Destinations     []DestinationStatus `json:"destinations"`
	Note             string              `json:"note"`
}

// TestResult is the outcome of one test delivery.
type TestResult struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
}

type destination struct {
	kind   string
	url    string
	secret string
	status DestinationStatus
}

type job struct {
	dest  *destination
	event Event
}

type tracked struct {
	Severity string `json:"severity"`
	Notified bool   `json:"notified"`
}

// Notifier tracks which incidents have been announced and delivers alerts in the background. A nil *Notifier is a
// valid, disabled notifier.
type Notifier struct {
	cfg    Config
	logger *slog.Logger
	client *http.Client
	queue  chan job
	done   chan struct{}
	wg     sync.WaitGroup
	now    func() time.Time

	mu    sync.Mutex
	dests []*destination
	known map[string]tracked
	open  map[string]IncidentRef
}

// New builds a notifier and starts its delivery worker. It returns (nil, nil) when no destination is configured.
func New(cfg Config, logger *slog.Logger) (*Notifier, error) {
	cfg.MinSeverity = normalizeSeverity(cfg.MinSeverity)
	cfg.SlackWebhookURL = strings.TrimSpace(cfg.SlackWebhookURL)
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.SlackWebhookURL == "" && cfg.WebhookURL == "" {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	n := &Notifier{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{
			Timeout: deliveryTimeout,
			// A webhook that redirects is not followed: the alert goes exactly where the operator pointed it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		queue: make(chan job, queueSize),
		done:  make(chan struct{}),
		now:   func() time.Time { return time.Now().UTC() },
		known: map[string]tracked{},
		open:  map[string]IncidentRef{},
	}
	if cfg.SlackWebhookURL != "" {
		n.dests = append(n.dests, &destination{kind: "slack", url: cfg.SlackWebhookURL,
			status: DestinationStatus{Kind: "slack", Target: redactURL(cfg.SlackWebhookURL)}})
	}
	if cfg.WebhookURL != "" {
		n.dests = append(n.dests, &destination{kind: "webhook", url: cfg.WebhookURL, secret: cfg.WebhookSecret,
			status: DestinationStatus{Kind: "webhook", Target: redactURL(cfg.WebhookURL), Signed: cfg.WebhookSecret != ""}})
	}
	n.wg.Add(1)
	go n.worker()
	return n, nil
}

// redactURL keeps the scheme and host and hides the path, which for Slack is the credential.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	if u.Path == "" || u.Path == "/" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// Close stops the delivery worker after the queued alerts are attempted (bounded by the delivery timeout).
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	select {
	case <-n.done:
		return
	default:
	}
	close(n.done)
	n.wg.Wait()
}

// Enabled reports whether at least one destination is configured.
func (n *Notifier) Enabled() bool { return n != nil && len(n.dests) > 0 }

func refOf(inc domain.Incident) IncidentRef {
	return IncidentRef{
		ID: inc.ID, Fingerprint: inc.Fingerprint, RuleID: inc.RuleID, RuleName: inc.RuleName, Category: inc.Category,
		Severity: string(inc.Severity), EvalStatus: string(inc.EvalStatus), EvidenceStrength: string(inc.EvidenceStrength),
		Title: inc.Title, Summary: inc.Summary, AffectedResources: append([]string(nil), inc.AffectedResources...),
		FirstSeenAt: inc.FirstSeenAt,
	}
}

// Sync compares the incidents open right now with what has already been announced and queues alerts for what
// opened, changed severity or (when allowResolve is true) resolved. allowResolve is false while a new collection
// session is still warming up, so incidents that are merely not re-evaluated yet are not reported as resolved.
// Because the record survives session restarts, restarting collection does not re-announce open incidents.
func (n *Notifier) Sync(open []domain.Incident, meta Meta, allowResolve bool) {
	if !n.Enabled() {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	floor := severityRank(n.cfg.MinSeverity)
	seen := make(map[string]bool, len(open))
	for _, inc := range open {
		fp := inc.Fingerprint
		if fp == "" {
			fp = inc.ID
		}
		seen[fp] = true
		sev := string(inc.Severity)
		ref := refOf(inc)
		prev, existed := n.known[fp]
		switch {
		case !existed:
			t := tracked{Severity: sev}
			if severityRank(sev) >= floor {
				t.Notified = true
				n.enqueueLocked(n.event(EventOpened, meta, &ref, now))
			}
			n.known[fp] = t
		case prev.Severity != sev:
			t := tracked{Severity: sev, Notified: prev.Notified}
			if prev.Notified {
				ev := n.event(EventSeverityChanged, meta, &ref, now)
				ev.PreviousSeverity = prev.Severity
				n.enqueueLocked(ev)
			} else if severityRank(sev) >= floor {
				t.Notified = true
				n.enqueueLocked(n.event(EventOpened, meta, &ref, now))
			}
			n.known[fp] = t
		}
		n.open[fp] = ref
	}
	if !allowResolve {
		return
	}
	gone := make([]string, 0)
	for fp := range n.known {
		if !seen[fp] {
			gone = append(gone, fp)
		}
	}
	sort.Strings(gone)
	for _, fp := range gone {
		t := n.known[fp]
		ref, ok := n.open[fp]
		if t.Notified && ok {
			ev := n.event(EventResolved, meta, &ref, now)
			if !ref.FirstSeenAt.IsZero() && now.After(ref.FirstSeenAt) {
				ev.DurationSeconds = now.Sub(ref.FirstSeenAt).Seconds()
			}
			n.enqueueLocked(ev)
		}
		delete(n.known, fp)
		delete(n.open, fp)
	}
}

func (n *Notifier) event(kind string, meta Meta, ref *IncidentRef, now time.Time) Event {
	ev := Event{
		Schema: Schema, Type: kind, DeliveryID: newID(), Timestamp: now, Cluster: meta.Cluster, DataMode: meta.DataMode,
		Emulated: meta.DataMode == "live_emulated", Incident: ref, GPULensVersion: meta.Version,
	}
	if n.cfg.PublicURL != "" {
		ev.ConsoleURL = n.cfg.PublicURL + "/?live_tab=incidents"
	}
	return ev
}

func (n *Notifier) enqueueLocked(ev Event) {
	for _, d := range n.dests {
		select {
		case n.queue <- job{dest: d, event: ev}:
		default:
			d.status.Dropped++
			n.logger.Warn("alert queue full; alert dropped", "destination", d.kind, "event", ev.Type)
		}
	}
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for {
		select {
		case j := <-n.queue:
			n.deliverWithRetry(j)
		case <-n.done:
			// Drain what is already queued, one attempt each.
			for {
				select {
				case j := <-n.queue:
					n.record(j.dest, j.event.Type, n.post(context.Background(), j.dest, j.event, deliveryTimeout))
				default:
					return
				}
			}
		}
	}
}

func (n *Notifier) deliverWithRetry(j job) {
	var err error
	backoff := time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = n.post(context.Background(), j.dest, j.event, deliveryTimeout)
		if err == nil || attempt == maxAttempts {
			break
		}
		select {
		case <-time.After(backoff):
		case <-n.done:
			attempt = maxAttempts
		}
		backoff *= 4
	}
	n.record(j.dest, j.event.Type, err)
	if err != nil {
		n.logger.Warn("alert delivery failed", "destination", j.dest.kind, "target", j.dest.status.Target, "event", j.event.Type, "error", err)
	}
}

func (n *Notifier) record(d *destination, eventType string, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := n.now()
	d.status.LastAttemptAt = &t
	d.status.LastEvent = eventType
	if err != nil {
		d.status.Failed++
		d.status.LastError = err.Error()
		return
	}
	d.status.Sent++
	d.status.LastSuccessAt = &t
	d.status.LastError = ""
}

func (n *Notifier) post(ctx context.Context, d *destination, ev Event, timeout time.Duration) error {
	var body []byte
	var err error
	if d.kind == "slack" {
		body, err = json.Marshal(SlackMessage(ev))
	} else {
		body, err = json.Marshal(ev)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not build the request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gpu-lens/"+strings.TrimPrefix(ev.GPULensVersion, "v"))
	if d.kind == "webhook" {
		req.Header.Set("X-GPULens-Event", ev.Type)
		req.Header.Set("X-GPULens-Delivery", ev.DeliveryID)
		if d.secret != "" {
			req.Header.Set("X-GPULens-Signature-256", "sha256="+Sign(d.secret, body))
		}
	}
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// Never echo the URL: for Slack it contains the credential.
			if ue.Timeout() {
				return errors.New("timed out")
			}
			return fmt.Errorf("request failed: %v", ue.Err)
		}
		return errors.New("request failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Sign returns the hex HMAC-SHA256 of body with secret. Receivers verify X-GPULens-Signature-256 = "sha256=" + this.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SendTest delivers a test alert to every destination right away (one attempt each) and reports the outcome.
func (n *Notifier) SendTest(meta Meta) []TestResult {
	if !n.Enabled() {
		return nil
	}
	ev := n.event(EventTest, meta, nil, n.now())
	ev.Message = "Test alert from GPU Lens. If you can read this, alert delivery works."
	out := make([]TestResult, 0, len(n.dests))
	for _, d := range n.dests {
		err := n.post(context.Background(), d, ev, testTimeout)
		n.record(d, EventTest, err)
		r := TestResult{Kind: d.kind, Target: d.status.Target, OK: err == nil}
		if err != nil {
			r.Error = err.Error()
		}
		out = append(out, r)
	}
	return out
}

// Status returns the current delivery record.
func (n *Notifier) Status() Status {
	if !n.Enabled() {
		return Status{Enabled: false, MinSeverity: "warning", Destinations: []DestinationStatus{},
			Note: "No alert destination is configured. Start GPU Lens with -alert-slack-webhook and/or -alert-webhook-url (or GPULENS_ALERT_SLACK_WEBHOOK_URL / GPULENS_ALERT_WEBHOOK_URL)."}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := Status{Enabled: true, MinSeverity: n.cfg.MinSeverity, PublicURL: n.cfg.PublicURL, TrackedIncidents: len(n.known)}
	for _, d := range n.dests {
		st.Destinations = append(st.Destinations, d.status)
	}
	st.Note = "Alerts are sent when a live incident at or above the minimum severity opens, changes severity or resolves. Destinations are set at startup only."
	return st
}

// Counters returns sent/failed/dropped totals per destination kind, for /metrics.
func (n *Notifier) Counters() map[string][3]int {
	out := map[string][3]int{}
	if !n.Enabled() {
		return out
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, d := range n.dests {
		out[d.kind] = [3]int{d.status.Sent, d.status.Failed, d.status.Dropped}
	}
	return out
}

// Snapshot returns the announced-incident record so it can be persisted across restarts.
func (n *Notifier) Snapshot() map[string]PersistedIncident {
	out := map[string]PersistedIncident{}
	if !n.Enabled() {
		return out
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for fp, t := range n.known {
		out[fp] = PersistedIncident{Severity: t.Severity, Notified: t.Notified, Incident: n.open[fp]}
	}
	return out
}

// PersistedIncident is one entry of the persisted announced-incident record.
type PersistedIncident struct {
	Severity string      `json:"severity"`
	Notified bool        `json:"notified"`
	Incident IncidentRef `json:"incident"`
}

// Restore loads a record saved by Snapshot (before collection starts).
func (n *Notifier) Restore(rec map[string]PersistedIncident) {
	if !n.Enabled() {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for fp, p := range rec {
		n.known[fp] = tracked{Severity: p.Severity, Notified: p.Notified}
		n.open[fp] = p.Incident
	}
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// SlackMessage renders an event as a Slack incoming-webhook message (text fallback plus Block Kit blocks).
func SlackMessage(ev Event) map[string]interface{} {
	prefix := ""
	if ev.Emulated {
		prefix = "[EMULATED] "
	}
	cluster := ev.Cluster
	if cluster == "" {
		cluster = "GPU Lens"
	}
	var headline, body string
	switch ev.Type {
	case EventTest:
		headline = "🧪 GPU Lens test alert"
		body = ev.Message
	default:
		inc := ev.Incident
		if inc == nil {
			inc = &IncidentRef{}
		}
		sev := strings.ToUpper(inc.Severity)
		switch ev.Type {
		case EventOpened:
			headline = fmt.Sprintf("%s Incident opened · %s", severityEmoji(inc.Severity), sev)
		case EventResolved:
			headline = "✅ Incident resolved"
			if ev.DurationSeconds > 0 {
				headline += " after " + humanDuration(ev.DurationSeconds)
			}
		case EventSeverityChanged:
			headline = fmt.Sprintf("%s Severity changed · %s → %s", severityEmoji(inc.Severity), strings.ToUpper(ev.PreviousSeverity), sev)
		}
		body = "*" + escapeSlack(inc.Title) + "*"
		if inc.Summary != "" {
			body += "\n" + escapeSlack(truncate(inc.Summary, 600))
		}
	}
	text := fmt.Sprintf("%s%s on %s", prefix, headline, cluster)
	contextBits := []string{"cluster `" + escapeSlack(cluster) + "`"}
	if ev.Incident != nil && ev.Incident.RuleID != "" {
		contextBits = append([]string{"rule `" + escapeSlack(ev.Incident.RuleID) + "`"}, contextBits...)
	}
	if ev.Emulated {
		contextBits = append(contextBits, "*EMULATED data (Live Lab), not real hardware*")
	}
	if ev.ConsoleURL != "" {
		contextBits = append(contextBits, "<"+ev.ConsoleURL+"|Open in GPU Lens>")
	}
	return map[string]interface{}{
		"text": text,
		"blocks": []interface{}{
			map[string]interface{}{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*" + escapeSlack(prefix+headline) + "*\n" + body}},
			map[string]interface{}{"type": "context", "elements": []interface{}{map[string]string{"type": "mrkdwn", "text": strings.Join(contextBits, " · ")}}},
		},
	}
}

func severityEmoji(sev string) string {
	switch sev {
	case "critical":
		return "🔴"
	case "warning":
		return "🟠"
	}
	return "🔵"
}

func escapeSlack(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func humanDuration(sec float64) string {
	switch {
	case sec < 90:
		return fmt.Sprintf("%.0fs", sec)
	case sec < 5400:
		return fmt.Sprintf("%.0f min", sec/60)
	default:
		return fmt.Sprintf("%.1f h", sec/3600)
	}
}
