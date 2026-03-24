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

	"github.com/apaqa/watchtower/internal/admin"
	"github.com/apaqa/watchtower/internal/agent"
	"github.com/apaqa/watchtower/internal/alert"
	"github.com/apaqa/watchtower/internal/anomaly"
	"github.com/apaqa/watchtower/internal/audit"
	"github.com/apaqa/watchtower/internal/auth"
	"github.com/apaqa/watchtower/internal/compare"
	"github.com/apaqa/watchtower/internal/config"
	"github.com/apaqa/watchtower/internal/correlation"
	"github.com/apaqa/watchtower/internal/dashboard"
	"github.com/apaqa/watchtower/internal/export"
	"github.com/apaqa/watchtower/internal/forecast"
	"github.com/apaqa/watchtower/internal/grafana"
	"github.com/apaqa/watchtower/internal/health"
	"github.com/apaqa/watchtower/internal/incident"
	"github.com/apaqa/watchtower/internal/ingest"
	"github.com/apaqa/watchtower/internal/logstore"
	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/notify"
	"github.com/apaqa/watchtower/internal/oncall"
	"github.com/apaqa/watchtower/internal/pipeline"
	"github.com/apaqa/watchtower/internal/plugin"
	"github.com/apaqa/watchtower/internal/probe"
	"github.com/apaqa/watchtower/internal/procmon"
	"github.com/apaqa/watchtower/internal/quota"
	"github.com/apaqa/watchtower/internal/registry"
	"github.com/apaqa/watchtower/internal/replay"
	"github.com/apaqa/watchtower/internal/savedquery"
	"github.com/apaqa/watchtower/internal/servicemap"
	"github.com/apaqa/watchtower/internal/slo"
	"github.com/apaqa/watchtower/internal/statuspage"
	"github.com/apaqa/watchtower/internal/synthetic"
	"github.com/apaqa/watchtower/internal/tags"
	"github.com/apaqa/watchtower/internal/tenant"
	"github.com/apaqa/watchtower/internal/tracestore"
	"github.com/apaqa/watchtower/internal/tsdb"
	"github.com/apaqa/watchtower/internal/webhook"
)

func main() {
	// ── 1. Load configuration (use defaults when file is missing) ─────────────
	cfg, report, err := config.LoadAndValidate("watchtower.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(os.Stderr, "config warning: %s\n", warning)
	}
	if report.HasErrors() {
		for _, validationErr := range report.Errors {
			fmt.Fprintf(os.Stderr, "config error: %s\n", validationErr)
		}
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
	compareEngine := compare.New(db)
	changeDetector := compare.NewChangeDetector(db)
	changeDetector.Start()
	defer changeDetector.Stop()
	forecaster := forecast.New(db)
	grafanaHandler := grafana.New(db, alertEng)

	// 初始化值班排班器和事故存储
	oncallScheduler := oncall.New()
	incidentStore := incident.New()
	incidentStore.SetAlertEngine(alertEng)
	incidentStore.SetOnCallFn(oncallScheduler.CurrentOnCallName)
	incidentStore.SetNotifyRouter(notifyRouter)
	incidentStore.Start()
	defer incidentStore.Stop()

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
			Role:        ak.Role,
			TenantID:    ak.TenantID,
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
	tagManager := tags.NewManager()
	savedQueryStore := savedquery.NewStore()
	replayManager, err := replay.New(db, "watchtower-data")
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay manager init failed: %v\n", err)
		os.Exit(1)
	}

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

	// 初始化审计日志存储
	auditStore := audit.New()

	// 初始化租户存储（预置 default 租户）
	tenantStore := tenant.New()

	// 初始化健康检查管理器（需在 probeMgr 之后，闭包捕获其变量）
	healthMgr := health.New()
	healthMgr.Register(health.NewFuncCheck("tsdb", func() health.HealthResult {
		if db == nil {
			return health.HealthResult{Status: health.StatusUnhealthy, Message: "TSDB 未初始化"}
		}
		return health.HealthResult{Status: health.StatusHealthy, Message: "ok"}
	}))
	healthMgr.Register(health.NewFuncCheck("ingest", func() health.HealthResult {
		return health.HealthResult{Status: health.StatusHealthy, Message: "ok"}
	}))
	healthMgr.Register(health.NewFuncCheck("probe_manager", func() health.HealthResult {
		_ = probeMgr // 确保探针管理器已初始化
		return health.HealthResult{Status: health.StatusHealthy, Message: "ok"}
	}))
	healthMgr.Start()
	defer healthMgr.Stop()

	// 初始化资源配额管理器
	quotaMgr := quota.NewManager()
	for _, qc := range cfg.Quotas {
		quotaMgr.UpdateLimit(quota.ResourceType(qc.Resource), qc.Limit)
	}

	// 初始化速率限制器（如已在配置中启用）
	var rateLimiter *quota.RateLimiter
	if cfg.RateLimit.Enabled {
		rlCap := cfg.RateLimit.Capacity
		rlRate := cfg.RateLimit.RefillRate
		if rlCap <= 0 {
			rlCap = quota.DefaultBucketConfig.Capacity
		}
		if rlRate <= 0 {
			rlRate = quota.DefaultBucketConfig.RefillRate
		}
		rateLimiter = quota.NewRateLimiter(quota.BucketConfig{Capacity: rlCap, RefillRate: rlRate})
	}

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
	ingestSrv.RegisterReplayManager(replayManager)
	ingestSrv.RegisterTagManager(tagManager)
	ingestSrv.RegisterSavedQueryStore(savedQueryStore)
	ingestSrv.RegisterNotifyRouter(notifyRouter)
	ingestSrv.RegisterServiceMapBuilder(svcMapBuilder)
	ingestSrv.RegisterSLOStore(sloStore)
	ingestSrv.RegisterPipeline(aggPipeline)
	ingestSrv.RegisterExportHandler(exportHandler)
	ingestSrv.RegisterProcessMonitor(procMonitor)
	ingestSrv.RegisterStatusPage(statusPage)
	ingestSrv.RegisterAnomalyDetector(anomalyDetector)
	ingestSrv.RegisterCorrelator(correlator)
	ingestSrv.RegisterCompareEngine(compareEngine, changeDetector)
	ingestSrv.RegisterForecaster(forecaster)
	ingestSrv.RegisterRetentionEngine(retentionEngine)
	ingestSrv.RegisterAdminHandler(adminHandler)
	ingestSrv.RegisterGrafanaHandler(grafanaHandler)
	ingestSrv.RegisterIncidentStore(incidentStore)
	ingestSrv.RegisterOncallScheduler(oncallScheduler)
	ingestSrv.RegisterAuditStore(auditStore)
	ingestSrv.RegisterTenantStore(tenantStore)
	ingestSrv.RegisterHealthManager(healthMgr)
	ingestSrv.RegisterQuotaManager(quotaMgr)
	if rateLimiter != nil {
		ingestSrv.RegisterRateLimiter(rateLimiter)
	}

	// 初始化插件管理器，postFn 直接写入 TSDB（无需经过 HTTP）
	pluginMgr := plugin.New(func(pts []model.MetricPoint) {
		db.Write(pts)
	})
	// 注册内置插件
	pluginMgr.Register(plugin.NewNetworkPlugin())
	pluginMgr.Register(plugin.NewGPUPlugin())
	pluginMgr.Register(plugin.NewDockerPlugin())

	// 从配置文件构建每个插件的配置映射
	pluginCfgs := make(map[string]map[string]string)
	pluginEnabled := map[string]bool{"network": true, "gpu": true, "docker": true}
	for _, pc := range cfg.Plugins {
		if pc.Enabled != nil && !*pc.Enabled {
			pluginEnabled[pc.Name] = false
			continue
		}
		pluginCfgs[pc.Name] = pc.Config
	}

	// 只启动未被禁用的插件
	enabledCfgs := make(map[string]map[string]string)
	for name, enabled := range pluginEnabled {
		if enabled {
			enabledCfgs[name] = pluginCfgs[name]
		}
	}
	pluginMgr.StartAll(enabledCfgs)
	defer pluginMgr.StopAll()
	ingestSrv.RegisterPluginManager(pluginMgr)

	// 初始化 Webhook 接收器（GitHub + 通用 + 自定义端点）
	webhookHandler := webhook.New(
		func(pts []model.MetricPoint) { db.Write(pts) },
		func(entries []model.LogEntry) { logStore.WriteMany(entries) },
	)
	for _, wc := range cfg.Webhooks {
		rules := make([]webhook.ExtractRule, len(wc.Rules))
		for i, r := range wc.Rules {
			rules[i] = webhook.ExtractRule{
				JSONPath:      r.JSONPath,
				MetricName:    r.MetricName,
				LabelMappings: r.LabelMappings,
			}
		}
		webhookHandler.AddConfig(webhook.WebhookConfig{
			Name:  wc.Name,
			Path:  wc.Path,
			Rules: rules,
		})
	}
	ingestSrv.RegisterWebhookHandler(webhookHandler)
	fmt.Printf("Webhooks: GitHub + Generic + %d custom endpoint(s)\n", len(cfg.Webhooks))

	// 初始化合成监控（postFn 直接写入 TSDB）
	synMonitor := synthetic.New(func(pts []model.MetricPoint) { db.Write(pts) })
	for _, stc := range cfg.SyntheticTests {
		steps := make([]synthetic.SyntheticStep, len(stc.Steps))
		for i, s := range stc.Steps {
			steps[i] = synthetic.SyntheticStep{
				Name:           s.Name,
				Method:         s.Method,
				URL:            s.URL,
				Headers:        s.Headers,
				Body:           s.Body,
				ExpectedStatus: s.ExpectedStatus,
				AssertContains: s.AssertContains,
				ExtractVars:    s.ExtractVars,
			}
		}
		if err := synMonitor.Add(synthetic.SyntheticTest{
			Name:     stc.Name,
			Interval: stc.Interval,
			Timeout:  stc.Timeout,
			Steps:    steps,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "failed to load synthetic test %q: %v\n", stc.Name, err)
		}
	}
	ingestSrv.RegisterSyntheticMonitor(synMonitor)
	defer synMonitor.StopAll()
	fmt.Printf("Synthetic: %d test(s) loaded\n", len(cfg.SyntheticTests))

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
	shareStore := dashboard.NewShareStore()
	dashSrv.SetShareStore(shareStore)
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
	fmt.Printf("Compare: %s/api/v1/compare?metric=cpu_usage_percent&current=1h&previous=1h\n", base)
	fmt.Printf("Changes: %s/api/v1/changes\n", base)
	fmt.Printf("Forecast:%s/api/v1/forecast?metric=cpu_usage_percent\n", base)
	fmt.Printf("Capacity:%s/api/v1/capacity\n", base)
	fmt.Printf("Retain:  %s/api/v1/retention\n", base)
	fmt.Printf("Admin:   %s/api/v1/admin/status\n", base)
	fmt.Printf("Grafana: %s/api/grafana/\n", base)
	fmt.Printf("Incidents:%s/api/v1/incidents\n", base)
	fmt.Printf("OnCall:  %s/api/v1/oncall\n", base)
	fmt.Printf("Health:  %s/api/v1/health\n", base)
	fmt.Printf("Live:    %s/api/v1/health/live\n", base)
	fmt.Printf("Ready:   %s/api/v1/health/ready\n", base)
	fmt.Printf("Quotas:  %s/api/v1/quotas\n", base)
	fmt.Printf("Plugins: %s/api/v1/plugins\n", base)
	fmt.Printf("Audit:   %s/api/v1/audit\n", base)
	fmt.Printf("Tenants: %s/api/v1/tenants\n", base)
	fmt.Printf("Tags:    %s/api/v1/tags\n", base)
	fmt.Printf("Queries: %s/api/v1/queries\n", base)
	fmt.Printf("Scrape:  %s/metrics  (Prometheus scrape endpoint)\n", base)
	fmt.Printf("Prom:    %s/api/v1/metrics/prometheus  (Prometheus push)\n", base)
	fmt.Printf("Panels:  http://localhost:%d/api/v1/dashboard/panels\n", cfg.Server.DashboardPort)
	fmt.Printf("Tpls:    http://localhost:%d/api/v1/dashboard/templates\n", cfg.Server.DashboardPort)
	fmt.Printf("Share:   http://localhost:%d/api/v1/dashboard/share\n", cfg.Server.DashboardPort)
	fmt.Printf("Replay:  %s/api/v1/replay/recordings\n", base)
	fmt.Printf("Webhook: %s/api/v1/webhook/github  (also /generic)\n", base)
	fmt.Printf("Synth:   %s/api/v1/synthetic\n", base)
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
