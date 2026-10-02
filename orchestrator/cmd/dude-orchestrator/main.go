// Command dude-orchestrator runs dude's work: the delivery workflow, the
// agents it starts on lux, and the pull requests it opens.
//
// It serves no users. The backend (apps/control-plane) is the public API; it
// calls this process's internal API to change what runs, and reads the
// shared database for everything it displays.
//
// Settings come from one TOML file shared with the backend (DUDE_CONFIG, else
// /etc/dude/dude.toml if present) and the environment, each variable
// overriding its file key; internal/config is the schema and
// docs/dude.example.toml lists every key. `dude-orchestrator validate`
// checks them as startup does and exits, connecting to nothing. Among them:
//
//	DATABASE_URL                 the shared Postgres, as the dude_app role
//	DUDE_ORCHESTRATOR_TOKEN      the service token the backend authenticates with
//	DUDE_ORCHESTRATOR_LISTEN     internal API address (default 127.0.0.1:3100)
//	LUX_URL, LUX_API_KEY         the lux control plane and a `run`-scoped key
//	LUX_CONSOLE_URL              lux's console, for terminal links (default: LUX_URL)
//	DUDE_AGENT_IMAGE             image for agents when a project names none
//	DUDE_LAYER_IMAGE             the dude layer the image library's images are finished with; unset,
//	                             a Run whose image is a library image fails before lux
//	DUDE_REGISTRY_AUTH           how lux logs in to pull agent and preview images: none (default), static, or ecr
//	                             (DUDE_AGENT_IMAGE's ECR registry, a token from the AWS default
//	                             credential chain, e.g. the instance role, minted fresh for each start)
//	DUDE_ECR_ROLE_ARN            ecr: a pull-only role to assume and mint tokens as (default: the host's credentials)
//	DUDE_REGISTRY, DUDE_REGISTRY_CREDENTIAL  static: the registry host and its user:password
//	DUDE_LLM_URL                 the LLM API's base URL for agents, before the API path (https://…/v1)
//	DUDE_LLM_KEY                 its key, given to agents as a secret env var
//	DUDE_PR_RECONCILE            how often open PRs are re-read as a backstop to webhooks (default 15m)
//	DUDE_PARK_AFTER/IDLE_AFTER   the grace before parking a Run waiting on a person, and the idle limit,
//	                             for projects that set none (durations; default: the delivery policy's)
//	DUDE_VAPID_PUBLIC_KEY/_PRIVATE_KEY  Web Push keys (default: made once, kept in the database)
//	DUDE_VAPID_SUBJECT           who push services may contact about this factory (mailto: or https:)
//	DUDE_FACTORY_LOGINS          comma-separated logins whose PR comments are the factory's own
//	DUDE_EMBEDDINGS_URL          an OpenAI-compatible embeddings API, before /embeddings (llm-proxy:
//	                             https://…/v1); default DUDE_LLM_URL; off, or neither set: memory is
//	                             searched by words alone
//	DUDE_EMBEDDINGS_KEY          its key (default DUDE_LLM_KEY): a deployment's own, kept as a secret
//	DUDE_EMBEDDINGS_MODEL        default gemini-embedding-2
//	DUDE_EMBEDDINGS_DIMENSIONS   default 768; the index's size, so another is a migration
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
	"github.com/marciomartins/dude/orchestrator/internal/objects"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
	"github.com/marciomartins/dude/orchestrator/internal/servers"
	"github.com/marciomartins/dude/orchestrator/internal/version"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		os.Exit(validate(os.Args[2:], config.Options{}, os.Stdout, os.Stderr))
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("orchestrator stopped", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, set, err := resolve(config.Options{})
	if cfg != nil {
		logConfig(log, cfg)
	}
	if missing := (missingError{}); errors.As(err, &missing) {
		fmt.Fprintln(os.Stderr, missing.Error())
		os.Exit(2)
	}
	if err != nil {
		return err
	}
	database, err := db.Open(ctx, set.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	agent := set.Agent
	if agent.LLMURL == "" {
		log.Warn("agents have no LLM: DUDE_LLM_URL is not set; only fake/ models can run")
	}
	if os.Getenv("DUDE_OPENCODE_AUTH") != "" || os.Getenv("DUDE_OPENCODE_CONFIG") != "" {
		log.Warn("DUDE_OPENCODE_AUTH and DUDE_OPENCODE_CONFIG are ignored: agents get their model access from DUDE_LLM_URL and DUDE_LLM_KEY")
	}
	registryLogin, err := registry.FromEnv(ctx, set.Registry.getenv, agent.DefaultImage, registry.WithLog(log))
	if err != nil {
		return fmt.Errorf("registry login: %w", err)
	}
	if registryLogin != nil {
		attrs := []any{"mode", set.Registry.Mode, "registry", registryLogin.Registry()}
		if by := registry.MintedBy(registryLogin); by != "" {
			attrs = append(attrs, "minted_by", by)
		}
		log.Info("agent images are pulled with a registry login", attrs...)
	}
	host, _ := os.Hostname()

	// Test fixtures serve HTTP API and git daemon on distinct gateway ports.
	forges := forge.Resolver{DB: database, TestGitHost: os.Getenv("DUDE_TEST_GITHUB_GIT_HOST")}
	runtime := workflow.New(database, fmt.Sprintf("orchestrator-%s-%d", host, os.Getpid()), log)
	store := &delivery.Store{DB: database}
	runtime.Register(delivery.Workflow(store, forges))

	signalWorkflow := func(ctx context.Context, org, wf, name string, payload any, key string) error {
		return runtime.Signal(ctx, org, wf, name, payload, key)
	}
	luxClient := lux.New(set.LuxURL, set.LuxKey)
	preview, err := previewModeOf(ctx, luxClient, set.PreviewDomain, log, time.Second)
	if err != nil {
		return err
	}
	switch {
	case preview.Relative:
		log.Info("branch previews wake on request", "mode", "relative")
	case preview.Domain != "":
		log.Info("branch previews wake on request", "mode", "full-name", "domain", preview.Domain)
	case preview.LuxOff:
		log.Warn("branch previews do not wake on request: lux has previews off (preview.domain)", "mode", "off")
	default:
		log.Warn("branch previews do not wake on request: lux has no preview domain (preview.domain) and previews.domain is unset", "mode", "off")
	}
	bucket, err := objects.New(set.Objects)
	if err != nil {
		log.Error("image storage", "error", err)
		os.Exit(2)
	}
	// A nil *S3 in the interface would not be a nil Store.
	var objectStore objects.Store
	if bucket != nil {
		objectStore = bucket
	}
	syncer := &phases.Syncer{
		DB: database, Lux: luxClient, Objects: objectStore,
		Forges: forges, Agent: agent, Registry: registryLogin, Log: log,
		ParkAfter: set.ParkAfter, IdleAfter: set.IdleAfter,
		DiffEvery: set.DiffEvery, MachineUSDPerHour: set.MachineUSDPerHour,
	}
	defer syncer.Stop()
	// Resumes whose timing an earlier process committed but never
	// published; in the background, so startup does not wait on it.
	go func() {
		pass, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if n := syncer.TimeUntimedResumes(pass); n > 0 {
			log.Info("timed resumes an earlier process left untimed", "resumes", n)
		}
	}()
	serverService := preview.service(database, luxClient, log, set.ConsoleURL)
	previews := &servers.Previews{Service: serverService, Forges: forges, DefaultImage: agent.DefaultImage,
		Layer: agent.Layer, Registry: registryLogin, ReapAfter: set.PreviewReapAfter}
	defer previews.Stop()
	pullRequests := &prs.Syncer{DB: database, Forges: forges, Signal: signalWorkflow, Log: log,
		FactoryLogins: set.FactoryLogins}

	embedder, notifier := memoryAndPush(set, database, log)
	indexer := &memory.Indexer{DB: database, Embedder: embedder, Log: log}

	// Each loop sleeps when idle and runs again at once while there is work.
	// A kick wakes them all: a person's action should take effect now, not
	// on the next tick.
	kick := make(chan struct{}, 1)
	// Workflow steps in flight at once. A step waiting on a slow forge holds
	// one slot, not the loop.
	stepSlots := make(chan struct{}, 32)
	loops := []loop{
		{"workflow", 250 * time.Millisecond, func(ctx context.Context) (int, error) { return runtime.Dispatch(ctx, stepSlots) }},
		{"phase-sync", time.Second, syncer.Sweep},
		{"phase-notifier", time.Second, func(ctx context.Context) (int, error) {
			return phases.NotifyFinished(ctx, database, func(ctx context.Context, org, wf, runID, status string) error {
				return runtime.Signal(ctx, org, wf, delivery.SignalPhaseFinished,
					map[string]string{"runId": runID, "status": status}, "phase-finished:"+runID)
			})
		}},
		{"previews", time.Second, previews.Sweep},
		{"artifacts", time.Second, (&phases.Artifacts{DB: database, Lux: luxClient, Log: log}).Sweep},
		// Each Run carries its own next read (every set.LuxCostEvery); the
		// loop only looks for the ones due.
		{"lux-cost", 15 * time.Second, (&phases.Costs{DB: database, Lux: luxClient, Log: log, Every: set.LuxCostEvery}).Sweep},
		{"webhooks", time.Second, pullRequests.ProcessDeliveries},
		{"notify", 2 * time.Second, notifier.Sweep},
		{"indexer", 5 * time.Second, indexer.Sweep},
		{"pr-reconciler", time.Minute, func(ctx context.Context) (int, error) {
			return pullRequests.Reconcile(ctx, set.ReconcileEvery)
		}},
	}
	var wakers []chan struct{}
	var wg sync.WaitGroup
	for _, l := range loops {
		wake := make(chan struct{}, 1)
		wakers = append(wakers, wake)
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.run(ctx, log, wake)
		}()
	}
	serverService.Kick = func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	// lux's feed: wakes, idleness and gone servers of wakeable previews.
	go (&servers.Feed{DB: database, Lux: luxClient, Log: log, Kick: serverService.Kick}).Run(ctx)
	go func() {
		for range kick {
			for _, w := range wakers {
				select {
				case w <- struct{}{}:
				default:
				}
			}
		}
	}()

	srv := &http.Server{
		Addr: set.Listen,
		Handler: (&api.Server{DB: database, Lux: luxClient, Workflow: runtime, Token: set.Token, Log: log,
			PushKeys: notifier.Keys, Forges: forges, PRs: pullRequests, Servers: serverService,
			Embedder: embedder, Indexer: indexer, Kick: serverService.Kick}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// dude's tools for agents, on a listener of their own: agents reach it
	// from lux's hosts (DUDE_TOOLS_URL is how they see it), so it is not the
	// internal API's loopback.
	var tools *http.Server
	if addr := set.ToolsListen; addr != "" {
		tools = &http.Server{Addr: addr, Handler: (&agenttools.Server{DB: database, Log: log, Embedder: embedder,
			Kick: serverService.Kick}).Handler(),
			ReadHeaderTimeout: 10 * time.Second}
		go func() {
			log.Info("agent tools listening", "addr", addr, "url", agent.ToolsURL)
			if err := tools.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("agent tools stopped", "error", err)
			}
		}()
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		if tools != nil {
			_ = tools.Shutdown(shutdown)
		}
	}()
	log.Info("orchestrator listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	wg.Wait()
	return nil
}

type previewMode struct {
	Domain   string
	Relative bool
	LuxOff   bool // lux reports previews: false
}

// service is the servers.Service startup runs, in this preview mode.
func (m previewMode) service(database *db.DB, luxClient *lux.HTTPClient, log *slog.Logger, consoleURL string) *servers.Service {
	return &servers.Service{DB: database, Lux: luxClient, Log: log, ConsoleURL: consoleURL, PreviewDomain: m.Domain, PreviewRelative: m.Relative}
}

func previewModeOf(ctx context.Context, c *lux.HTTPClient, configured string, log *slog.Logger, retry time.Duration) (previewMode, error) {
	for {
		capability, err := luxPreviewDomain(ctx, c)
		if err == nil {
			luxDomain := capability.Domain
			if configured != "" && luxDomain != "" && configured != luxDomain {
				return previewMode{}, fmt.Errorf("previews.domain (DUDE_PREVIEW_DOMAIN) is %s but lux serves previews under %s", configured, luxDomain)
			}
			if capability.Previews != nil && !*capability.Previews {
				return previewMode{LuxOff: true}, nil
			}
			if capability.Previews != nil {
				if configured != "" && luxDomain == "" {
					log.Info("DUDE_PREVIEW_DOMAIN ignored for naming: lux accepts relative preview hostnames")
				}
				return previewMode{Relative: true}, nil
			}
			if configured != "" {
				luxDomain = configured
			}
			return previewMode{Domain: luxDomain}, nil
		}
		if errors.Is(err, lux.ErrNoServers) {
			return previewMode{}, fmt.Errorf("lux at startup: %w", err)
		}
		log.Warn("lux at startup: not answering; asking again", "in", retry, "error", err)
		select {
		case <-ctx.Done():
			return previewMode{}, fmt.Errorf("lux at startup: %w", err)
		case <-time.After(retry):
		}
		retry = min(2*retry, time.Minute)
	}
}

func luxPreviewDomain(ctx context.Context, c *lux.HTTPClient) (lux.PreviewConfig, error) {
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := lux.RequireServers(check, c); err != nil {
		return lux.PreviewConfig{}, err
	}
	d, err := c.PreviewDomain(check)
	if err != nil {
		return lux.PreviewConfig{}, fmt.Errorf("whoami: %w", err)
	}
	return d, nil
}

// memoryAndPush builds the embedder (nil when embeddings are off) and the
// Web Push notifier from set.
func memoryAndPush(set settings, database *db.DB, log *slog.Logger) (embeddings.Embedder, *notify.Notifier) {
	var embedder embeddings.Embedder
	if emb := set.Embeddings; emb.URL != "" {
		embedder = &embeddings.Client{BaseURL: emb.URL, Key: emb.Key,
			ModelName: set.EmbeddingsModel, Dims: set.EmbeddingsDimension}
		log.Info("memory searches by meaning", "model", embedder.Model(), "url", emb.URL,
			"url_from", emb.URLFrom, "key_from", emb.KeyFrom)
	} else {
		log.Info("memory searches by words only", "reason", emb.Off)
	}
	notifier := &notify.Notifier{DB: database, Log: log,
		Subject:   set.VAPIDSubject,
		PublicKey: set.VAPIDPublic, PrivateKey: set.VAPIDPrivate,
	}
	return embedder, notifier
}

type loop struct {
	name     string
	interval time.Duration
	sweep    func(context.Context) (int, error)
}

func (l loop) run(ctx context.Context, log *slog.Logger, wake <-chan struct{}) {
	for ctx.Err() == nil {
		n, err := l.sweep(ctx)
		if err != nil && ctx.Err() == nil {
			// A failing sweep must not end the loop, or the system silently
			// stops doing that job.
			log.Warn("loop failed", "loop", l.name, "error", err)
		}
		if n > 0 && err == nil {
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-time.After(l.interval):
		}
	}
}
