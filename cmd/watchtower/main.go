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
	"github.com/apaqa/watchtower/internal/admin"
	"github.com/apaqa/watchtower/internal/anomaly"
	"github.com/apaqa/watchtower/internal/auth"
	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/correlation"
	"github.com/apaqa/watchtower/internal/forecast"
	"github.com/apaqa/watchtower/internal/grafana"
	"github.com/apaqa/watchtower/internal/dashboard"
	"github.com/apaqa/watchtower/internal/export"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/notify"
	"github.com/apaqa/watchtower/internal/pipeline"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/procmon"
	"github.com/apaqa/watchtower/internal/registry"
	"github.com/apaqa/watchtower/internal/servicemap"
	"github.com/apaqa/watchtower/internal/slo"
	"github.com/apaqa/watchtower/internal/statuspage"
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

	// ── 3b. Build notification router from config ─────────────────────────────
	notifyRouter := notify.NewRouter()
	for _, ch := range cfg.Notifications.Channels {
		var channel notify.Channel
		switch ch.Type {
		case "console":
			channel = notify.NewConsoleChannel()
		case "webhook":
			if ch.URL != "" {
				channel = notify.NewWebhookChannel(ch.URL)
			}
		case "slack":
			if ch.URL != "" {
				channel = notify.NewSlackChannel(ch.URL)
			}
		case "discord":
			if ch.URL != "" {
				channel = notify.NewDiscordChannel(ch.URL)
			}
		case "email":
			if ch.SMTPHost != "" && ch.From != "" && len(ch.To) > 0 {
				port := ch.SMTPPort
				if port == 0 {
					port = 587
				}
				channel = notify.NewEmailChannel(ch.SMTPHost, port, ch.From, ch.To, ch.SMTPUsername, ch.SMTPPassword)
			}
		}
		if channel != nil {
			notifyRouter.AddChannel(channel, ch.Severities)
		}
	}
	alertEng.SetRouter(notifyRouter)
	fmt.Printf("Notify: %d channel(s) configured\n", len(cfg.Notifications.Channels))

	// ── 4. Initialize log store ───────────────────────────────────────────────
	logStore := logstore.New()
	defer logStore.Stop()

	// ── 5. Initialize trace store ─────────────────────────────────────────────
	traceStore := tracestore.New()
	defer traceStore.Stop()

	// ── 5d. Initialize service map builder, SLO store, pipeline, export, process monitor ──
	svcMapBuilder := servicemap.NewBuilder(traceStore)
	sloStore := slo.NewStore(db)
	aggPipeline := pipeline.New(db)
	aggPipeline.Start()
	defer aggPipeline.Stop()
	exportHandler := export.New(db, logStore, traceStore)
	procMonitor := procmon.New(db)
	procMonitor.Start()
	defer procMonitor.Stop()
	anomalyDetector := anomaly.New(db)
	anomalyDetector.Start()
	defer anomalyDetector.Stop()
	correlator := correlation.New(db)
	forecaster := forecast.New(db)
	grafanaHandler := grafana.New(db, alertEng)

	// 初始化降采样器和保留策略引擎
	downsampler := tsdb.NewDownsampler(db)
	downsampler.Start()
	defer downsampler.Stop()
	retentionEngine := tsdb.NewRetentionEngine(db)
	// 从配置文件加载自定义保留策略
	for _, rpc := range cfg.RetentionPolicies {
		if err := retentionEngine.AddPolicy(tsdb.RetentionPolicy{
			Name:         rpc.Name,
			MatchPattern: rpc.MatchPattern,
			MaxAgeSecs:   rpc.MaxAgeSecs,
			MaxPoints:    rpc.MaxPoints,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to load retention policy %q: %v\n", rpc.Name, err)
		}
	}
	retentionEngine.Start()
	defer retentionEngine.Stop()

	// 初始化 Admin Handler
	adminHandler := admin.New(db, cfg, retentionEngine, "watchtower.yaml")

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
	statusPage := statuspage.New(probeMgr, sloStore, alertEng)

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
	ingestSrv.RegisterNotifyRouter(notifyRouter)
	ingestSrv.RegisterServiceMapBuilder(svcMapBuilder)
	ingestSrv.RegisterSLOStore(sloStore)
	ingestSrv.RegisterPipeline(aggPipeline)
	ingestSrv.RegisterExportHandler(exportHandler)
	ingestSrv.RegisterProcessMonitor(procMonitor)
	ingestSrv.RegisterStatusPage(statusPage)
	ingestSrv.RegisterAnomalyDetector(anomalyDetector)
	ingestSrv.RegisterCorrelator(correlator)
	ingestSrv.RegisterForecaster(forecaster)
	ingestSrv.RegisterRetentionEngine(retentionEngine)
	ingestSrv.RegisterAdminHandler(adminHandler)
	ingestSrv.RegisterGrafanaHandler(grafanaHandler)
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
	fmt.Printf("Notify:  %s/api/v1/notifications\n", base)
	fmt.Printf("SvcMap:  %s/api/v1/servicemap\n", base)
	fmt.Printf("SLOs:    %s/api/v1/slos\n", base)
	fmt.Printf("Pipeline:%s/api/v1/pipeline/rules\n", base)
	fmt.Printf("Export:  %s/api/v1/export/metrics  (also /logs /traces)\n", base)
	fmt.Printf("Procs:   %s/api/v1/processes\n", base)
	fmt.Printf("Status:  %s/status\n", base)
	fmt.Printf("Anomaly: %s/api/v1/anomalies\n", base)
	fmt.Printf("Correl:  %s/api/v1/correlate?a=cpu_usage_percent&b=memory_usage_percent&window=30m\n", base)
	fmt.Printf("Forecast:%s/api/v1/forecast?metric=cpu_usage_percent\n", base)
	fmt.Printf("Capacity:%s/api/v1/capacity\n", base)
	fmt.Printf("Retain:  %s/api/v1/retention\n", base)
	fmt.Printf("Admin:   %s/api/v1/admin/status\n", base)
	fmt.Printf("Grafana: %s/api/grafana/\n", base)
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
