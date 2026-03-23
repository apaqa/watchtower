// WatchTower monitoring platform entry point.
// A single binary starts the ingest API, dashboard, system agent, log agent,
// trace agent, endpoint probe manager, and alert engine.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apaqa/watchtower/internal/agent"
	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/auth"
	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/dashboard"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/registry"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
)

func main() {
	// ── 1. Load configuration (use defaults when file is missing) ─────────────
	cfg, err := config.Load("watchtower.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("config loaded — ingest port: %d, dashboard port: %d, endpoint probes: %d\n",
		cfg.Server.IngestPort, cfg.Server.DashboardPort, len(cfg.Endpoints))

	// ── 2. Initialize TSDB with disk persistence ──────────────────────────────
	db, err := tsdb.NewWithStorage("watchtower-data")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TSDB init failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Stop()

	// ── 3. Initialize alert engine and pre-load rules from config ─────────────
	alertEng := alert.NewEngine(db)
	for _, ac := range cfg.Alerts {
		if err := alertEng.AddRule(alert.AlertRule{
			Name:       ac.Name,
			Expression: ac.Expression,
			Operator:   ac.Operator,
			Threshold:  ac.Threshold,
			Duration:   ac.Duration,
			Severity:   ac.Severity,
			WebhookURL: ac.WebhookURL,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to load alert rule %q: %v\n", ac.Name, err)
		}
	}
	alertEng.Start()
	defer alertEng.Stop()

	// ── 4. Initialize log store ───────────────────────────────────────────────
	logStore := logstore.New()
	defer logStore.Stop()

	// ── 5. Initialize trace store ─────────────────────────────────────────────
	traceStore := tracestore.New()
	defer traceStore.Stop()

	// ── 4b. Initialize API key store and pre-load keys from config ───────────
	keyStore := auth.NewKeyStore()
	for _, ak := range cfg.APIKeys {
		if ak.Key == "" || ak.Name == "" {
			continue
		}
		perms := ak.Permissions
		if len(perms) == 0 {
			perms = []string{auth.PermRead, auth.PermWrite}
		}
		keyStore.Add(auth.APIKey{
			Key:         ak.Key,
			Name:        ak.Name,
			Permissions: perms,
		})
	}
	if keyStore.Count() > 0 {
		fmt.Printf("Auth: %d API key(s) loaded — all /api/ endpoints are protected\n", keyStore.Count())
	} else {
		fmt.Println("Auth: no keys configured — running in open mode")
	}

	// ── 5b. Initialize agent registry ────────────────────────────────────────
	agentRegistry := registry.New()
	defer agentRegistry.Stop()

	// ── 5c. Initialize custom panel store ────────────────────────────────────
	panelStore := dashboard.NewPanelStore()

	// ── 6. Initialize probe manager and pre-load endpoints from config ────────
	probeMgr := probe.NewManager(db)
	for _, ep := range cfg.Endpoints {
		if err := probeMgr.AddProbe(probe.ProbeConfig{
			Name:           ep.Name,
			URL:            ep.URL,
			Method:         ep.Method,
			ExpectedStatus: ep.ExpectedStatus,
			IntervalSecs:   ep.IntervalSeconds,
			TimeoutMs:      ep.TimeoutMs,
			Headers:        ep.Headers,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to load endpoint probe %q: %v\n", ep.Name, err)
		}
	}
	defer probeMgr.Stop()

	// ── 7. Start ingest server (metrics + WQL + alerts + logs + probes + traces) ──
	ingestSrv, err := ingest.New(cfg.IngestAddr(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ingest server failed to start: %v\n", err)
		os.Exit(1)
	}
	ingestSrv.RegisterAlertEngine(alertEng)
	ingestSrv.RegisterLogStore(logStore)
	ingestSrv.RegisterProbeManager(probeMgr)
	ingestSrv.RegisterTraceStore(traceStore)
	ingestSrv.RegisterAgentRegistry(agentRegistry)
	ingestSrv.RegisterKeyStore(keyStore)
	go func() {
		if err := ingestSrv.Start(); err != nil {
			// http.ErrServerClosed is expected on clean shutdown
		}
	}()

	// ── 8. Start dashboard server (Web UI + WebSocket) ────────────────────────
	dashSrv, err := dashboard.New(cfg.DashboardAddr(), db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dashboard server failed to start: %v\n", err)
		os.Exit(1)
	}
	dashSrv.SetAlertEngine(alertEng)
	dashSrv.SetLogStore(logStore)
	dashSrv.SetPanelStore(panelStore)
	go func() {
		if err := dashSrv.Start(); err != nil {
			// expected on clean shutdown
		}
	}()

	// ── 9. Brief pause to ensure ingest server is ready before starting agents ─
	time.Sleep(100 * time.Millisecond)

	if cfg.Agent.Enabled {
		// System metrics agent
		ingestURL := fmt.Sprintf("http://localhost:%d/api/v1/metrics", cfg.Server.IngestPort)
		ag, err := agent.New(ingestURL, time.Duration(cfg.Agent.IntervalSeconds)*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "metrics agent init failed: %v\n", err)
			os.Exit(1)
		}
		registryURL := fmt.Sprintf("http://localhost:%d/api/v1/agents/register", cfg.Server.IngestPort)
		ag.SetRegistryURL(registryURL)
		go ag.Start()
		defer ag.Stop()

		// Log agent
		logURL := fmt.Sprintf("http://localhost:%d/api/v1/logs/single", cfg.Server.IngestPort)
		logAg := agent.NewLogAgent(logURL, 10*time.Second)
		logAg.Start()
		defer logAg.Stop()

		// Trace agent
		traceURL := fmt.Sprintf("http://localhost:%d/api/v1/traces", cfg.Server.IngestPort)
		traceAg := agent.NewTraceAgent(traceURL, 10*time.Second)
		go traceAg.Start()
		defer traceAg.Stop()
	}

	// ── 10. Print startup summary ─────────────────────────────────────────────
	base := fmt.Sprintf("http://localhost:%d", cfg.Server.IngestPort)
	fmt.Printf("WatchTower started — Dashboard: http://localhost:%d\n", cfg.Server.DashboardPort)
	fmt.Printf("Ingest:  %s/api/v1/metrics\n", base)
	fmt.Printf("WQL:     %s/api/v1/query?q=avg(cpu_usage_percent[5m])\n", base)
	fmt.Printf("Alerts:  %s/api/v1/alerts/rules\n", base)
	fmt.Printf("Logs:    %s/api/v1/logs\n", base)
	fmt.Printf("Probes:  %s/api/v1/probes\n", base)
	fmt.Printf("Traces:  %s/api/v1/traces\n", base)
	fmt.Printf("Agents:  %s/api/v1/agents\n", base)
	fmt.Printf("Auth:    %s/api/v1/auth/keys\n", base)
	fmt.Printf("Scrape:  %s/metrics  (Prometheus scrape endpoint)\n", base)
	fmt.Printf("Prom:    %s/api/v1/metrics/prometheus  (Prometheus push)\n", base)
	fmt.Printf("Panels:  http://localhost:%d/api/v1/dashboard/panels\n", cfg.Server.DashboardPort)
	fmt.Println("Press Ctrl+C to exit")

	// ── 11. Wait for termination signal then shut down gracefully ─────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Println("\nShutting down WatchTower…")
	_ = dashSrv.Close()
	_ = ingestSrv.Close()
	fmt.Println("Goodbye.")
}
