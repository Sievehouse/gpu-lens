package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gpulens/gpu-lens/internal/api"
	"github.com/gpulens/gpu-lens/internal/config"
	"github.com/gpulens/gpu-lens/internal/rules"
	"github.com/gpulens/gpu-lens/internal/source"
	"github.com/gpulens/gpu-lens/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.DefaultConfig()

	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listen address")
	flag.StringVar(&cfg.Source.Mode, "mode", cfg.Source.Mode, "Data source mode: fixture or live")
	flag.StringVar(&cfg.Source.FixturesDir, "fixtures-dir", cfg.Source.FixturesDir, "Directory containing deterministic JSON fixtures")
	flag.StringVar(&cfg.Source.DefaultScenarioID, "scenario", cfg.Source.DefaultScenarioID, "Default offline scenario ID")
	flag.StringVar(&cfg.StaticDir, "static-dir", cfg.StaticDir, "Directory containing compiled frontend assets")
	flag.StringVar(&cfg.SQLitePath, "state-file", cfg.SQLitePath, "Path to local incident state file")
	flag.Parse()

	if envListen := os.Getenv("GPULENS_LISTEN_ADDR"); envListen != "" {
		cfg.ListenAddr = envListen
	}
	if envScenario := os.Getenv("GPULENS_DEFAULT_SCENARIO"); envScenario != "" {
		cfg.Source.DefaultScenarioID = envScenario
	}
	if envFixtures := os.Getenv("GPULENS_FIXTURES_DIR"); envFixtures != "" {
		cfg.Source.FixturesDir = envFixtures
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
	_ = httpServer.Shutdown(ctx)
}
