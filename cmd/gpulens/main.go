package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gpulens/gpu-lens/internal/api"
	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/live"
	"github.com/gpulens/gpu-lens/internal/rules"
	"github.com/gpulens/gpu-lens/internal/source"
	"github.com/gpulens/gpu-lens/internal/store"
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
	var noLive, noSMI, noHost, showVersion bool
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
	flag.BoolVar(&noHost, "no-host-vitals", false, "Do not read local host vitals (/proc, /sys)")
	flag.BoolVar(&lc.AllowRemoteConfig, "live-allow-remote-config", false, "Allow non-local browsers to change live connection settings")
	flag.BoolVar(&noLive, "no-live", false, "Disable the live console entirely (simulated demo only)")
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit")
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

	liveEngine := live.NewEngine(cfg, logger)
	apiServer.SetLiveEngine(liveEngine)
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
}
