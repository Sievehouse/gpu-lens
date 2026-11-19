// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The GPU Lens Authors

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sievehouse/gpu-lens/internal/alert"
	"github.com/Sievehouse/gpu-lens/internal/api"
	"github.com/Sievehouse/gpu-lens/internal/audit"
	"github.com/Sievehouse/gpu-lens/internal/config"
	"github.com/Sievehouse/gpu-lens/internal/fleet"
	"github.com/Sievehouse/gpu-lens/internal/live"
	"github.com/Sievehouse/gpu-lens/internal/rules"
	"github.com/Sievehouse/gpu-lens/internal/source"
	"github.com/Sievehouse/gpu-lens/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.DefaultConfig()

	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listen address")
	flag.StringVar(&cfg.Source.Mode, "mode", cfg.Source.Mode, "Startup mode: fixture (simulated demo first) or live (start live collection at boot)")
	flag.StringVar(&cfg.Source.FixturesDir, "fixtures-dir", cfg.Source.FixturesDir, "Directory containing deterministic JSON fixtures")
	flag.StringVar(&cfg.Source.DefaultScenarioID, "scenario", cfg.Source.DefaultScenarioID, "Default offline scenario ID")
	flag.StringVar(&cfg.StaticDir, "static-dir", cfg.StaticDir, "Directory containing compiled frontend assets")
	flag.StringVar(&cfg.SQLitePath, "state-file", cfg.SQLitePath, "Path to local incident state file")

	// Live console ("Go Live"). Every setting also has a GPULENS_* environment variable, which wins over the flag.
	lc := &cfg.Live
	var dcgmURL, promURL, k8sURL string
	var noLive, noSMI, noAMD, noHost, showVersion bool
	flag.Float64Var(&lc.IntervalSeconds, "live-interval", lc.IntervalSeconds, "Live refresh interval in seconds (1-300)")
	flag.Float64Var(&lc.IdleTimeoutSeconds, "live-idle-timeout", lc.IdleTimeoutSeconds, "Stop live collection after this many seconds without a viewer (0 = never)")
	flag.StringVar(&lc.ClusterName, "cluster-name", lc.ClusterName, "Name shown for the live cluster / host")
	flag.StringVar(&dcgmURL, "dcgm-url", "", "dcgm-exporter metrics URL, e.g. http://gpu-node:9400/metrics (default: probe "+config.DefaultDCGMExporterEndpoint+")")
	flag.StringVar(&promURL, "prometheus-url", "", "Prometheus base URL, e.g. http://prometheus:9090 (default: probe "+config.DefaultPrometheusEndpoint+")")
	flag.StringVar(&lc.Prometheus.TokenFile, "prometheus-token-file", "", "File holding a bearer token for Prometheus")
	flag.StringVar(&k8sURL, "kubernetes-url", "", "Kubernetes API server URL (default: in-cluster service account, else probe kubectl proxy at "+config.DefaultKubectlProxyEndpoint+")")
	flag.StringVar(&lc.Kubernetes.TokenFile, "kubernetes-token-file", "", "File holding a bearer token for the Kubernetes API")
	flag.StringVar(&lc.Kubernetes.CAFile, "kubernetes-ca-file", "", "PEM CA bundle that signed the Kubernetes API server certificate")
	flag.StringVar(&lc.NvidiaSMIPath, "nvidia-smi-path", "", "Path to nvidia-smi (default: search PATH and well-known locations)")
	flag.BoolVar(&noSMI, "no-nvidia-smi", false, "Do not query the local NVIDIA driver through nvidia-smi")
	flag.StringVar(&lc.AMDSMIPath, "amd-smi-path", "", "Path to amd-smi for AMD GPUs (default: search PATH and /opt/rocm/bin) — experimental")
	flag.BoolVar(&noAMD, "no-amd-smi", false, "Do not query the local AMD driver through amd-smi")
	flag.BoolVar(&noHost, "no-host-vitals", false, "Do not read local host vitals (/proc, /sys)")
	flag.BoolVar(&lc.AllowRemoteConfig, "live-allow-remote-config", false, "Allow non-local browsers to change live connection settings")
	flag.BoolVar(&noLive, "no-live", false, "Disable the live console entirely (simulated demo only)")
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit")

	// Access control, audit, durable state and alerting. Secrets are better passed as files or environment
	// variables than as flags, because command lines are visible to other users of the machine.
	var auth api.AuthConfig
	var adminPassFile, viewerPassFile, auditFile, liveStateFile, webhookSecretFile string
	var alertCfg alert.Config
	flag.StringVar(&auth.AdminUser, "basic-auth-user", "", "Require HTTP basic auth: admin user name (full access)")
	flag.StringVar(&auth.AdminPass, "basic-auth-pass", "", "Admin password (prefer -basic-auth-pass-file or GPULENS_BASIC_AUTH_PASS)")
	flag.StringVar(&adminPassFile, "basic-auth-pass-file", "", "File holding the admin password")
	flag.StringVar(&auth.ViewerUser, "viewer-user", "", "Optional read-only viewer user name (needs the admin account to be set)")
	flag.StringVar(&auth.ViewerPass, "viewer-pass", "", "Viewer password (prefer -viewer-pass-file or GPULENS_VIEWER_PASS)")
	flag.StringVar(&viewerPassFile, "viewer-pass-file", "", "File holding the viewer password")
	flag.StringVar(&auditFile, "audit-log-file", "", "Append the audit log (who changed what) to this JSON-lines file")
	flag.StringVar(&liveStateFile, "live-state-file", "", "Keep live thresholds, resolved incidents and alert history in this file across restarts")
	flag.StringVar(&alertCfg.SlackWebhookURL, "alert-slack-webhook", "", "Slack incoming-webhook URL for incident alerts (https)")
	flag.StringVar(&alertCfg.WebhookURL, "alert-webhook-url", "", "Generic webhook URL: incident alerts are POSTed as JSON (schema gpulens.alert/v1)")
	flag.StringVar(&alertCfg.WebhookSecret, "alert-webhook-secret", "", "Sign webhook bodies with HMAC-SHA256 (header X-GPULens-Signature-256)")
	flag.StringVar(&webhookSecretFile, "alert-webhook-secret-file", "", "File holding the webhook signing secret")
	flag.StringVar(&alertCfg.MinSeverity, "alert-min-severity", "warning", "Lowest incident severity that is alerted: info, warning or critical")
	flag.StringVar(&alertCfg.PublicURL, "public-url", "", "External URL of this console, used for links in alerts, e.g. https://gpulens.example.com")
	var peerFlags multiFlag
	var fleetInterval float64
	flag.Var(&peerFlags, "peer", "Another GPU Lens instance to show on the Fleet tab, as name=https://host:8080 (repeatable; user:pass@ in the URL for basic auth)")
	flag.Float64Var(&fleetInterval, "fleet-interval", 15, "Seconds between polls of each fleet peer")
	flag.Parse()

	if showVersion {
		fmt.Println("gpu-lens", live.Version)
		return
	}

	if dcgmURL != "" {
		lc.DCGM = config.LiveSourceConfig{Enabled: true, Explicit: true, Endpoint: dcgmURL}
	}
	if promURL != "" {
		lc.Prometheus.Enabled, lc.Prometheus.Explicit, lc.Prometheus.Endpoint = true, true, promURL
	}
	if k8sURL != "" {
		lc.Kubernetes.Enabled, lc.Kubernetes.Explicit, lc.Kubernetes.Endpoint = true, true, k8sURL
	}
	if noSMI {
		lc.NvidiaSMIEnabled = false
	}
	if noAMD {
		lc.AMDSMIEnabled = false
	}
	if noHost {
		lc.HostVitals = false
	}
	if noLive {
		lc.Enabled = false
	}

	if envListen := os.Getenv("GPULENS_LISTEN_ADDR"); envListen != "" {
		cfg.ListenAddr = envListen
	}
	if envScenario := os.Getenv("GPULENS_DEFAULT_SCENARIO"); envScenario != "" {
		cfg.Source.DefaultScenarioID = envScenario
	}
	if envFixtures := os.Getenv("GPULENS_FIXTURES_DIR"); envFixtures != "" {
		cfg.Source.FixturesDir = envFixtures
	}
	if envMode := strings.TrimSpace(os.Getenv("GPULENS_MODE")); envMode != "" {
		cfg.Source.Mode = envMode
	}
	envStr(&auth.AdminUser, "GPULENS_BASIC_AUTH_USER")
	envStr(&auth.AdminPass, "GPULENS_BASIC_AUTH_PASS")
	envStr(&adminPassFile, "GPULENS_BASIC_AUTH_PASS_FILE")
	envStr(&auth.ViewerUser, "GPULENS_VIEWER_USER")
	envStr(&auth.ViewerPass, "GPULENS_VIEWER_PASS")
	envStr(&viewerPassFile, "GPULENS_VIEWER_PASS_FILE")
	envStr(&auditFile, "GPULENS_AUDIT_LOG_FILE")
	envStr(&liveStateFile, "GPULENS_LIVE_STATE_FILE")
	envStr(&alertCfg.SlackWebhookURL, "GPULENS_ALERT_SLACK_WEBHOOK_URL")
	envStr(&alertCfg.WebhookURL, "GPULENS_ALERT_WEBHOOK_URL")
	envStr(&alertCfg.WebhookSecret, "GPULENS_ALERT_WEBHOOK_SECRET")
	envStr(&webhookSecretFile, "GPULENS_ALERT_WEBHOOK_SECRET_FILE")
	envStr(&alertCfg.MinSeverity, "GPULENS_ALERT_MIN_SEVERITY")
	envStr(&alertCfg.PublicURL, "GPULENS_PUBLIC_URL")
	if v := strings.TrimSpace(os.Getenv("GPULENS_PEERS")); v != "" {
		peerFlags = append(peerFlags, v)
	}
	peers, err := fleet.ParsePeers(peerFlags)
	if err != nil {
		logger.Error("invalid fleet peers", "error", err)
		os.Exit(1)
	}
	for _, f := range []struct {
		path string
		dest *string
		what string
	}{{adminPassFile, &auth.AdminPass, "admin password"}, {viewerPassFile, &auth.ViewerPass, "viewer password"}, {webhookSecretFile, &alertCfg.WebhookSecret, "webhook secret"}} {
		if f.path == "" {
			continue
		}
		v, err := readSecretFile(f.path)
		if err != nil {
			logger.Error("cannot read "+f.what+" file", "path", f.path, "error", err)
			os.Exit(1)
		}
		*f.dest = v
	}
	if err := validateAuth(auth); err != nil {
		logger.Error("invalid authentication settings", "error", err)
		os.Exit(1)
	}
	lc.ApplyEnv(os.Getenv)
	lc.DetectInCluster(os.Getenv, nil)
	cfg.ApplyLegacySourceFields()
	lc.AutoStart = cfg.Source.Mode == "live"
	if lc.AutoStart {
		// Started as a live service: keep collecting with no browser attached unless a timeout was asked for.
		idleConfigured := os.Getenv("GPULENS_LIVE_IDLE_TIMEOUT_SECONDS") != ""
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "live-idle-timeout" {
				idleConfigured = true
			}
		})
		if !idleConfigured {
			lc.IdleTimeoutSeconds = 0
		}
	}

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid startup configuration", "error", err)
		os.Exit(1)
	}

	fixtureSource, err := source.NewFixtureSource(cfg.Source.FixturesDir, cfg.Source.DefaultScenarioID)
	if err != nil {
		logger.Error("failed to initialize offline fixture source", "fixtures_dir", cfg.Source.FixturesDir, "error", err)
		os.Exit(1)
	}

	engine := rules.NewDefaultEngine()
	incidentStore := store.NewIncidentStore(cfg.SQLitePath)
	apiServer := api.NewServer(cfg, fixtureSource, engine, incidentStore, logger)

	auditLog, err := audit.New(auditFile)
	if err != nil {
		logger.Error("cannot open the audit log file", "path", auditFile, "error", err)
		os.Exit(1)
	}
	apiServer.SetAuth(auth)
	apiServer.SetAudit(auditLog)
	if !auth.Enabled() && !listensOnLoopbackOnly(cfg.ListenAddr) {
		logger.Warn("authentication is off and GPU Lens is reachable from other machines: anyone who can reach it can change live thresholds. Set -basic-auth-user/-basic-auth-pass-file or put it behind an authenticating proxy (docs/AUTH.md).",
			"listen_addr", cfg.ListenAddr)
	}

	notifier, err := alert.New(alertCfg, logger)
	if err != nil {
		logger.Error("invalid alert settings", "error", err)
		os.Exit(1)
	}
	defer notifier.Close()

	liveEngine := live.NewEngine(cfg, logger)
	liveEngine.SetAlerter(notifier)
	if err := liveEngine.SetStateFile(liveStateFile); err != nil {
		logger.Warn("live state file could not be loaded; starting with a fresh state", "path", liveStateFile, "error", err)
	}
	saverStop := make(chan struct{})
	saverDone := make(chan struct{})
	go func() {
		defer close(saverDone)
		if liveStateFile == "" {
			<-saverStop
			return
		}
		liveEngine.RunStateSaver(30*time.Second, saverStop)
	}()
	apiServer.SetLiveEngine(liveEngine)
	hub, err := fleet.NewHub(liveEngine, peers, time.Duration(fleetInterval*float64(time.Second)), 5*time.Second)
	if err != nil {
		logger.Error("cannot start the fleet hub", "error", err)
		os.Exit(1)
	}
	apiServer.SetFleet(hub)
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	go hub.Run(hubCtx)
	if cfg.Live.Enabled && cfg.Live.AutoStart {
		// Unattended live mode keeps collecting without a browser attached.
		if err := liveEngine.Start(0); err != nil {
			logger.Error("failed to start live collection", "error", err)
			os.Exit(1)
		}
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiServer,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("starting GPU Lens server",
			"listen_addr", cfg.ListenAddr,
			"mode", cfg.Source.Mode,
			"default_scenario", cfg.Source.DefaultScenarioID,
			"live_console", cfg.Live.Enabled,
			"live_auto_start", cfg.Live.AutoStart,
			"read_only_enforced", true,
			"version", live.Version,
			"auth", auth.Enabled(),
			"viewer_account", auth.ViewerUser != "",
			"alerts", notifier.Enabled(),
			"audit_log_file", auditFile != "",
			"live_state_file", liveStateFile != "",
			"fleet_peers", len(peers),
		)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server terminated", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	logger.Info("shutting down GPU Lens server gracefully")
	liveEngine.Stop("GPU Lens is shutting down.")
	_ = httpServer.Shutdown(ctx)
	close(saverStop)
	<-saverDone
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func envStr(dest *string, key string) {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		*dest = v
	}
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}

func validateAuth(a api.AuthConfig) error {
	if (a.AdminUser == "") != (a.AdminPass == "") {
		return fmt.Errorf("set both the admin user and the admin password, or neither")
	}
	if (a.ViewerUser == "") != (a.ViewerPass == "") {
		return fmt.Errorf("set both the viewer user and the viewer password, or neither")
	}
	if a.ViewerUser != "" && !a.Enabled() {
		return fmt.Errorf("a viewer account needs an admin account (-basic-auth-user) as well")
	}
	if a.ViewerUser != "" && a.ViewerUser == a.AdminUser {
		return fmt.Errorf("the viewer and admin accounts must have different user names")
	}
	if a.Enabled() && len(a.AdminPass) < 8 {
		return fmt.Errorf("the admin password must be at least 8 characters")
	}
	return nil
}

func listensOnLoopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
