package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"ai-analyst-local/internal/activation"
	"ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/httpapi"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/publicapi"
	"ai-analyst-local/internal/chat/queryadapters"
	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/runpreflight"
	searchsvc "ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/db"
	dbmigrate "ai-analyst-local/internal/db/migrate"
	"ai-analyst-local/internal/githubcli"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/proxy"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/server"
	"ai-analyst-local/internal/sharepoint"
	"ai-analyst-local/internal/slack"
	"ai-analyst-local/internal/snowflake"
	"ai-analyst-local/internal/sources"
	"ai-analyst-local/internal/trino"
	"ai-analyst-local/internal/webui"
)

const (
	dockerDropUID = 65532
	dockerDropGID = 65532
)

func homeDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("HOME")); v != "" {
		return v, nil
	}
	return os.UserHomeDir()
}

func defaultConfigDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}

	// Respect XDG when set (Linux standard); otherwise use ~/.config.
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "aianalyst"), nil
}

func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		return strings.Contains(s, "docker") || strings.Contains(s, "kubepods") || strings.Contains(s, "containerd")
	}
	return false
}

func ensureWritableDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	// We need actual write permission, not just existence.
	f, err := os.CreateTemp(dir, ".writetest-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

func ensureDockerHomeAndConfig(cfgDir string) (canDrop bool, _ error) {
	// If we're running as root in a container, fix permissions on the config dir.
	if os.Geteuid() != 0 || !isContainer() {
		return false, ensureWritableDir(cfgDir)
	}

	home, err := homeDir()
	if err != nil {
		return false, err
	}

	// Ensure home + ~/.config exist (some minimal images don't ship them).
	if err := os.MkdirAll(home, 0o755); err != nil {
		return false, err
	}
	canDrop = true
	if err := os.Chown(home, dockerDropUID, dockerDropGID); err != nil {
		canDrop = false
	}

	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		return false, err
	}
	if err := os.Chown(xdg, dockerDropUID, dockerDropGID); err != nil {
		canDrop = false
	}

	// Ensure cfgDir exists and is owned by the runtime user (handles root-owned Docker volumes).
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return false, err
	}
	if err := os.Chown(cfgDir, dockerDropUID, dockerDropGID); err != nil {
		canDrop = false
	}
	if err := os.Chmod(cfgDir, 0o700); err != nil {
		canDrop = false
	}

	// If the directory is still not writable, surface a clear error.
	if err := ensureWritableDir(cfgDir); err != nil {
		return false, fmt.Errorf("config dir not writable: %w", err)
	}
	return canDrop, nil
}

func openMySQL(ctx context.Context, dsn string) (*sql.DB, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return nil, nil
	}

	dbConn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	// Reasonable defaults; callers can always tune via DSN / upstream DB config.
	dbConn.SetConnMaxLifetime(30 * time.Minute)
	dbConn.SetMaxOpenConns(25)
	dbConn.SetMaxIdleConns(25)

	// MySQL in Docker may not be ready instantly even after container start.
	deadline := time.Now().Add(30 * time.Second)
	for {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = dbConn.PingContext(pctx)
		cancel()
		if err == nil {
			return dbConn, nil
		}
		if time.Now().After(deadline) {
			_ = dbConn.Close()
			return nil, fmt.Errorf("db ping: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func runSubLLMSummary(ctx context.Context, factory *providers.Factory, text string, instruction string, modelOverride string) (string, error) {
	if factory == nil {
		return "", errors.New("provider factory is required")
	}
	attr, _ := usage.AttributionFromContext(ctx)
	attr.Operation = usage.OperationRLMSubLLM
	ctx = usage.WithAttribution(ctx, attr)
	prov, err := factory.CreateProviderForContext(ctx, modelOverride, providerHostedToolsDisabledPolicy())
	if err != nil {
		return "", err
	}
	model := strings.TrimSpace(prov.Model())
	if model == "" {
		return "", errors.New("model is required")
	}
	payload := strings.Join([]string{
		"Summarize the provided text for retrieval use.",
		"Be concise and factual. Do not invent details.",
		"Instruction:",
		strings.TrimSpace(instruction),
		"",
		"Text:",
		strings.TrimSpace(text),
	}, "\n")
	user := payload
	msg := types.ChatMessage{
		Role:    types.RoleUser,
		Content: &user,
	}
	stream, err := prov.StreamChat(ctx, types.ChatRequest{
		Model:        model,
		SystemPrompt: "You are a concise retrieval summarizer.",
		Messages:     []types.ChatMessage{msg},
		Tools:        nil,
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for ev := range stream {
		switch ev.Event {
		case types.EventTextDelta:
			if d, ok := ev.Data.(types.TextDeltaData); ok {
				out.WriteString(d.Content)
			}
		case types.EventFinal:
			if d, ok := ev.Data.(types.FinalData); ok {
				if strings.TrimSpace(d.Answer) != "" {
					out.Reset()
					out.WriteString(d.Answer)
				}
			}
		case types.EventError:
			if d, ok := ev.Data.(types.ErrorData); ok {
				return "", errors.New(d.Message)
			}
		}
	}
	summary := strings.TrimSpace(out.String())
	if summary == "" {
		return "", errors.New("empty sub_llm output")
	}
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	return summary, nil
}

func providerHostedToolsDisabledPolicy() providers.HostedToolPolicy {
	disabled := map[string]bool{}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		if id := strings.TrimSpace(capability.ID); id != "" {
			disabled[id] = true
		}
	}
	return providers.HostedToolPolicy{Disabled: disabled}
}

func resolveQueryJobsRegistry(candidates ...any) any {
	for _, candidate := range candidates {
		if reg := queryJobsFromCandidate(candidate); reg != nil {
			return reg
		}
	}
	return nil
}

func queryJobsFromCandidate(candidate any) any {
	if candidate == nil {
		return nil
	}
	v := reflect.ValueOf(candidate)
	if !v.IsValid() {
		return nil
	}
	if reg := queryJobsFromValue(v); reg != nil {
		return reg
	}
	if v.Kind() != reflect.Pointer && v.CanAddr() {
		if reg := queryJobsFromValue(v.Addr()); reg != nil {
			return reg
		}
	}
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		if reg := queryJobsFromValue(v.Elem()); reg != nil {
			return reg
		}
	}
	return nil
}

func queryJobsFromValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	for _, methodName := range []string{"QueryJobs", "QueryJobRegistry", "JobsRegistry", "AsyncQueryJobs", "JobRegistry"} {
		method := v.MethodByName(methodName)
		if !method.IsValid() {
			continue
		}
		mt := method.Type()
		if mt.NumIn() != 0 {
			continue
		}
		if mt.NumOut() == 1 {
			res := method.Call(nil)[0]
			if !res.IsValid() || (res.Kind() == reflect.Pointer && res.IsNil()) {
				continue
			}
			return res.Interface()
		}
		if mt.NumOut() == 2 {
			errType := reflect.TypeOf((*error)(nil)).Elem()
			if !mt.Out(1).Implements(errType) {
				continue
			}
			out := method.Call(nil)
			if !out[1].IsNil() {
				continue
			}
			res := out[0]
			if !res.IsValid() || (res.Kind() == reflect.Pointer && res.IsNil()) {
				continue
			}
			return res.Interface()
		}
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	for _, fieldName := range []string{"QueryJobs", "QueryJobRegistry", "JobsRegistry", "AsyncQueryJobs", "JobRegistry"} {
		f := v.FieldByName(fieldName)
		if !f.IsValid() {
			continue
		}
		if !f.CanInterface() {
			continue
		}
		if f.Kind() == reflect.Pointer && f.IsNil() {
			continue
		}
		return f.Interface()
	}
	return nil
}

func main() {
	var host string
	var port int
	var dev bool
	var cfgDir string
	var cfgDirFlag string
	var dbConfig string
	var maxConcurrentRuns int
	var desktopToken string

	// If you ship this in Docker, prefer `--host 0.0.0.0`.
	flag.StringVar(&host, "host", "127.0.0.1", "host to listen on")
	flag.IntVar(&port, "port", 8787, "port to listen on")
	flag.BoolVar(&dev, "dev", false, "development mode (allows Vite origin)")
	flag.StringVar(&cfgDirFlag, "config-dir", "", "config directory for settings/secrets (default ~/.config/aianalyst)")
	flag.StringVar(&dbConfig, "db-config", "", "database config path (default: config-dir/db.json) OR a MySQL DSN string; enables DB-backed thread/run/artifact APIs")
	flag.IntVar(&maxConcurrentRuns, "max-concurrent-runs", optionc.DefaultMaxConcurrentRuns, "maximum concurrent chat runs (Option C); additional runs queue")
	flag.StringVar(&desktopToken, "desktop-token", "", "per-launch desktop authorization token for local native shells")
	flag.Parse()

	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if cfgDirFlag != "" {
		cfgDir = cfgDirFlag
	} else {
		var err error
		cfgDir, err = defaultConfigDir()
		if err != nil {
			log.Fatalf("config dir: %v", err)
		}
	}

	// In Docker, we start as root, fix ownership on the mounted config volume, then drop privileges.
	canDrop, err := ensureDockerHomeAndConfig(cfgDir)
	if err != nil {
		log.Fatalf("config dir: %v", err)
	}
	if canDrop {
		if err := dropToNonrootInContainer(); err != nil {
			log.Fatalf("drop privileges: %v", err)
		}
	} else if os.Geteuid() == 0 && isContainer() {
		log.Printf("Warning: could not adjust ownership for %s; continuing as root inside container.", cfgDir)
	}

	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		log.Fatalf("settings store: %v", err)
	}

	secretsStore, err := secrets.NewStore(cfgDir)
	if err != nil {
		log.Fatalf("secrets store: %v", err)
	}

	activationStore, err := activation.NewStore(cfgDir, activation.Options{DevBypass: dev && !isContainer()})
	if err != nil {
		log.Fatalf("activation store: %v", err)
	}

	proxyHandler := proxy.NewHandler(settingsStore, secretsStore)
	bqHandler := bigquery.NewHandler(settingsStore, secretsStore)
	dbkHandler := databricks.NewHandler(settingsStore, secretsStore)
	trinoHandler := trino.NewHandler(settingsStore, secretsStore)
	snowflakeHandler := snowflake.NewHandler(settingsStore, secretsStore)

	// Optional DB-backed chat runtime (Option C).
	var dbConn *sql.DB
	{
		// Interpret dbConfig as either a config file path or a DSN.
		dsn := strings.TrimSpace(dbConfig)
		if dsn == "" {
			// If no flag is provided, try config-dir/db.json (best-effort).
			if cfg, _, err := db.LoadConfig(cfgDir, ""); err == nil {
				if v, err := cfg.MySQL.DSN(); err == nil {
					dsn = v
				}
			}
		} else {
			// If the flag points to an existing file (absolute or relative to cfgDir), treat it as JSON config.
			if cfg, _, err := db.LoadConfig(cfgDir, dsn); err == nil {
				if v, err := cfg.MySQL.DSN(); err == nil {
					dsn = v
				}
			}
		}

		if strings.TrimSpace(dsn) != "" {
			var err error
			dbConn, err = openMySQL(rootCtx, dsn)
			if err != nil {
				log.Fatalf("db open: %v", err)
			}
			if err := (dbmigrate.Migrator{FS: dbmigrate.EmbeddedMigrations}).Up(dbConn); err != nil {
				log.Fatalf("db migrate: %v", err)
			}
			defer func() { _ = dbConn.Close() }()
		}
	}

	var chatStore store.Store
	var usagePrimary usage.Repository
	if dbConn != nil {
		chatStore = store.NewMySQL(dbConn)
		usagePrimary = usage.NewMySQLRepository(dbConn)
		log.Printf("Chat store: MySQL")
	} else {
		jsonStore, err := store.NewJSON(cfgDir)
		if err != nil {
			log.Fatalf("chat store (json): %v", err)
		}
		chatStore = jsonStore
		usagePrimary, err = usage.NewJSONRepository(cfgDir)
		if err != nil {
			log.Fatalf("usage store (json): %v", err)
		}
		log.Printf("Chat store: JSON (%s)", filepath.Join(cfgDir, "chatdb"))
	}
	usageRecorder, err := usage.NewRecorder(cfgDir, usagePrimary)
	if err != nil {
		log.Fatalf("usage accounting journal: %v", err)
	}
	journalDrained := true
	if replayed, replayErr := usageRecorder.ReconcileAll(rootCtx); replayErr != nil {
		journalDrained = false
		log.Printf("usage: journal reconciliation deferred: %v", replayErr)
	} else if replayed > 0 {
		log.Printf("usage: replayed %d journal record(s)", replayed)
	}
	// Reconcile only pre-journal legacy rows after journal replay has restored
	// richer crash snapshots to the primary repository.
	if journalDrained {
		if reconciled, err := usage.NewService(usagePrimary).ReconcileRunning(rootCtx, time.Now().UTC()); err != nil {
			log.Printf("usage: failed to reconcile interrupted invocations: %v", err)
		} else if reconciled > 0 {
			log.Printf("usage: marked %d invocation(s) interrupted after restart", reconciled)
		}
	}
	usage.StartReconciler(rootCtx, usageRecorder, 15*time.Second)
	var usageRepository usage.Repository = usageRecorder
	if imported, backfillErr := tools.BackfillImageGenerationUsage(rootCtx, chatStore, usageRepository, settingsStore.Get()); backfillErr != nil {
		log.Printf("usage: image generation backfill deferred: %v", backfillErr)
		startImageUsageBackfillRetry(rootCtx, chatStore, usageRepository, settingsStore)
	} else if imported > 0 {
		log.Printf("usage: imported %d historical image generation invocation(s)", imported)
	}
	baseChatStore := chatStore
	longHorizonStore, ok := baseChatStore.(store.LongHorizonRepository)
	if !ok {
		log.Fatalf("chat store does not implement durable long-horizon query orchestration")
	}
	permissionsService, err := permissions.NewService(cfgDir, baseChatStore)
	if err != nil {
		log.Fatalf("permissions service: %v", err)
	}

	searchService, err := searchsvc.NewService(cfgDir, baseChatStore)
	if err != nil {
		log.Fatalf("search service: %v", err)
	}
	systemThreads := searchsvc.NewSystemThreadRegistry(cfgDir)
	chatStore = searchsvc.NewIndexedStore(baseChatStore, searchService)

	projectStore, err := projects.NewStore(cfgDir)
	if err != nil {
		log.Fatalf("projects store: %v", err)
	}
	sourceStore, err := sources.NewStore(cfgDir)
	if err != nil {
		log.Fatalf("source store: %v", err)
	}
	providerFactory := providers.NewFactory(settingsStore, secretsStore, usageRepository)
	atlassianManager := atlassian.NewManager(cfgDir, settingsStore)
	browserControlManager := browsercontrol.NewManager(cfgDir, settingsStore)
	sharePointManager := sharepoint.NewManager(cfgDir, settingsStore)
	slackManager := slack.NewManager(settingsStore, secretsStore)
	arxivManager := arxiv.NewManager(arxiv.Options{CfgDir: cfgDir, Settings: settingsStore})
	githubManager := githubcli.NewManager(githubcli.Options{
		Settings: settingsStore,
		Secrets:  secretsStore,
		Projects: projectStore,
		Sources:  sourceStore,
		CreateProject: func(ctx context.Context, name string, rootRelPath string) (githubcli.ProjectRecord, error) {
			project, err := chatStore.CreateProject(ctx, name, rootRelPath)
			if err != nil {
				return githubcli.ProjectRecord{}, err
			}
			return githubcli.ProjectRecord{
				ID:          project.ID,
				Name:        project.Name,
				RootRelPath: project.RootRelPath,
				FileCount:   project.FileCount,
				TotalBytes:  project.TotalBytes,
			}, nil
		},
		RefreshProjectIndex: func(ctx context.Context, projectID string) (any, error) {
			stats, err := projectStore.ScanFiles(ctx, projectID)
			if err != nil {
				return nil, err
			}
			files := make([]store.ProjectFileStat, 0, len(stats))
			for _, stat := range stats {
				files = append(files, store.ProjectFileStat{
					Path:     stat.Path,
					ByteSize: stat.ByteSize,
				})
			}
			project, err := chatStore.ReplaceProjectFiles(ctx, projectID, files)
			if err != nil {
				return nil, err
			}
			return project, nil
		},
	})
	rlmRuntime := rlm.NewRuntime(rlm.Options{
		Search: searchService,
		Store:  chatStore,
		CfgDir: cfgDir,
		SubLLM: func(ctx context.Context, text string, instruction string, modelOverride string) (string, error) {
			return runSubLLMSummary(ctx, providerFactory, text, instruction, modelOverride)
		},
	})
	toolReg := tools.NewRegistry(&tools.RegistryOptions{
		Search:     searchService,
		Store:      chatStore,
		CfgDir:     cfgDir,
		Sources:    sourceStore,
		RLM:        rlmRuntime,
		Atlassian:  atlassianManager,
		Browser:    browserControlManager,
		SharePoint: sharePointManager,
		GitHub:     githubManager,
		Slack:      slackManager,
		Arxiv:      arxivManager,
		Settings:   settingsStore,
		Secrets:    secretsStore,
	})
	queryPolicy := config.DefaultDatabaseQueryPolicy()
	if configured := settingsStore.Get().DatabaseQueryPolicy; configured != nil {
		queryPolicy = *configured
	}
	queryMaterializer := store.NewQueryMaterializer(longHorizonStore)
	queryJobs := jobs.NewRegistry(jobs.RegistryOptions{
		Store: longHorizonStore,
		Admission: &jobs.AdmissionConfig{
			MaxGlobal:        queryPolicy.MaxExecutingGlobal,
			MaxPerThread:     queryPolicy.MaxExecutingPerThread,
			MaxPerConnection: queryPolicy.MaxExecutingPerConnection,
		},
		Policy: &jobs.PolicyConfig{
			SoftQueue:     time.Duration(queryPolicy.SoftQueueTimeoutMilliseconds) * time.Millisecond,
			SoftExecution: time.Duration(queryPolicy.SoftExecutionTimeoutMilliseconds) * time.Millisecond,
			HardExecution: time.Duration(queryPolicy.HardExecutionTimeoutMilliseconds) * time.Millisecond,
			DefaultBudgets: jobs.Budgets{
				SoftQueue:      time.Duration(queryPolicy.SoftQueueTimeoutMilliseconds) * time.Millisecond,
				SoftExecution:  time.Duration(queryPolicy.SoftExecutionTimeoutMilliseconds) * time.Millisecond,
				HardExecution:  time.Duration(queryPolicy.HardExecutionTimeoutMilliseconds) * time.Millisecond,
				MaxResultRows:  queryPolicy.MaxResultRows,
				MaxResultBytes: queryPolicy.MaxMaterializedBytes,
			},
		},
		Scheduler: &jobs.SchedulerConfig{
			MaxInterval:     time.Duration(queryPolicy.MaxPollIntervalMilliseconds) * time.Millisecond,
			MaxErrorBackoff: time.Duration(queryPolicy.MonitorErrorBackoffMilliseconds) * time.Millisecond,
		},
		Materializer: queryMaterializer,
		Logf:         log.Printf,
	})
	queryJobs.RegisterAdapter(queryadapters.NewBigQuery(bqHandler))
	queryJobs.RegisterAdapter(queryadapters.NewDatabricks(dbkHandler))
	queryJobs.RegisterAdapter(queryadapters.NewSnowflake(snowflakeHandler))
	queryJobs.RegisterAdapter(queryadapters.NewTrino(trinoHandler))
	if stats, err := queryJobs.Rehydrate(rootCtx); err != nil {
		log.Fatalf("query job recovery: %v", err)
	} else if stats.Adopted+stats.Terminal+stats.Orphaned+stats.Expired+stats.Reconciling > 0 {
		log.Printf("Query job recovery: adopted=%d terminal=%d orphaned=%d expired=%d reconciling=%d deferred=%d",
			stats.Adopted, stats.Terminal, stats.Orphaned, stats.Expired, stats.Reconciling, stats.Deferred)
	}
	queryJobs.StartReaper(rootCtx, 5*time.Minute)
	runMgr := optionc.NewManager(chatStore, settingsStore, providerFactory, toolReg, projectStore, bqHandler, dbkHandler, trinoHandler, cfgDir, &optionc.Options{
		MaxConcurrentRuns: maxConcurrentRuns,
		QueryJobs:         queryJobs,
		LongHorizon:       longHorizonStore,
		SnowflakeHTTP:     snowflakeHandler,
		Permissions:       permissionsService,
		Sources:           sourceStore,
		Usage:             usageRepository,
	})
	runMgr.StartLongHorizonRecovery(rootCtx)
	optionc.StartJobWorker(rootCtx, chatStore, providerFactory, &optionc.WorkerOptions{Usage: usageRepository})

	chatHandler := httpapi.New(httpapi.Deps{
		Store:                chatStore,
		Manager:              runMgr,
		Projects:             projectStore,
		Search:               searchService,
		SystemThreads:        systemThreads,
		Settings:             settingsStore,
		Secrets:              secretsStore,
		Permissions:          permissionsService,
		Sources:              sourceStore,
		GitHub:               githubManager,
		CfgDir:               cfgDir,
		RunPreflightResolver: runpreflight.NewResolver(settingsStore, chatStore, permissionsService),
	})
	publicAPIHandler := publicapi.New(publicapi.Deps{
		Store:    chatStore,
		Manager:  runMgr,
		Settings: settingsStore,
		CfgDir:   cfgDir,
	})
	usageHandler := usage.NewHTTPHandler(usage.NewService(usageRepository))

	var static http.Handler
	if dist, err := webui.Dist(); err == nil {
		static = server.SPAFileServer(dist)
	}

	handler := server.NewHandler(server.HandlerOpts{
		Dev:             dev,
		DesktopToken:    desktopToken,
		Settings:        settingsStore,
		Secrets:         secretsStore,
		ChatStore:       chatStore,
		Proxy:           proxyHandler,
		BigQuery:        bqHandler,
		Databricks:      dbkHandler,
		Trino:           trinoHandler,
		Snowflake:       snowflakeHandler,
		Atlassian:       atlassianManager,
		SharePoint:      sharePointManager,
		GitHub:          githubManager,
		Permissions:     permissionsService,
		Slack:           slackManager,
		Arxiv:           arxivManager,
		Activation:      activationStore,
		DB:              dbConn,
		Chat:            chatHandler,
		PublicAPI:       publicAPIHandler,
		Usage:           usageHandler,
		UsageRepository: usageRepository,
		Static:          static,
	})

	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("AI Analyst (Local) server listening on http://%s", ln.Addr().String())
	log.Printf("Settings: %s", settingsStore.Path())
	log.Printf("Secrets:   %s", secretsStore.Path())
	if dev {
		log.Printf("Dev mode enabled: allows Origin http://localhost:5173")
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	case <-rootCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			_ = srv.Close()
		}
		if err := <-serveErr; err != nil && err != http.ErrServerClosed {
			log.Printf("serve stopped with error: %v", err)
		}
	}
}

func startImageUsageBackfillRetry(ctx context.Context, chatStore store.Store, repository usage.Repository, settingsStore *config.Store) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				imported, err := tools.BackfillImageGenerationUsage(ctx, chatStore, repository, settingsStore.Get())
				if err != nil {
					log.Printf("usage: image generation backfill retry deferred: %v", err)
					continue
				}
				if imported > 0 {
					log.Printf("usage: imported %d historical image generation invocation(s) after retry", imported)
				}
				return
			}
		}
	}()
}
