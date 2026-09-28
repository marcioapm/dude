// Command dude-orchestrator runs dude's work: the delivery workflow, the
// agents it starts on lux, and the pull requests it opens.
//
// It serves no users. The backend (apps/control-plane) is the public API; it
// calls this process's internal API to change what runs, and reads the
// shared database for everything it displays.
//
//	DATABASE_URL                 the shared Postgres, as the dude_app role
//	DUDE_ORCHESTRATOR_TOKEN      the service token the backend authenticates with
//	DUDE_ORCHESTRATOR_LISTEN     internal API address (default 127.0.0.1:3100)
//	LUX_URL, LUX_API_KEY         the lux control plane and a `run`-scoped key
//	DUDE_AGENT_IMAGE             image for agents when a project names none
//	DUDE_OPENCODE_AUTH/_CONFIG   OpenCode credentials (default: this machine's)
//	DUDE_PR_RECONCILE            how often open PRs are re-read as a backstop to webhooks (default 15m)
//	DUDE_PARK_AFTER/IDLE_AFTER   the grace before parking a Run waiting on a person, and the idle limit,
//	                             for projects that set none (durations; default: the delivery policy's)
//	DUDE_VAPID_PUBLIC_KEY/_PRIVATE_KEY  Web Push keys (default: made once, kept in the database)
//	DUDE_VAPID_SUBJECT           who push services may contact about this factory (mailto: or https:)
//	DUDE_FACTORY_LOGINS          comma-separated logins whose PR comments are the factory's own
//	DUDE_EMBEDDINGS_URL          an OpenAI-compatible embeddings API, before /embeddings (llm-proxy:
//	                             https://…/v1); unset, memory is searched by words alone
//	DUDE_EMBEDDINGS_KEY          its key: a deployment's own, kept as a secret
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/memory"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
	"github.com/marciomartins/dude/orchestrator/internal/phases"
	"github.com/marciomartins/dude/orchestrator/internal/prs"
	"github.com/marciomartins/dude/orchestrator/internal/version"
	"github.com/marciomartins/dude/orchestrator/internal/workflow"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version.Version)
		return
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

	database, err := db.Open(ctx, require("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer database.Close()

	agent, err := phases.LoadAgentConfig()
	if err != nil {
		return fmt.Errorf("agent configuration: %w", err)
	}
	reconcileEvery, err := time.ParseDuration(env("DUDE_PR_RECONCILE", "15m"))
	if err != nil {
		return fmt.Errorf("DUDE_PR_RECONCILE: %w", err)
	}
	parkAfter, err := time.ParseDuration(env("DUDE_PARK_AFTER", "0s"))
	if err != nil {
		return fmt.Errorf("DUDE_PARK_AFTER: %w", err)
	}
	idleAfter, err := time.ParseDuration(env("DUDE_IDLE_AFTER", "0s"))
	if err != nil {
		return fmt.Errorf("DUDE_IDLE_AFTER: %w", err)
	}
	// How often a working agent's diff is read besides after its edits.
	diffEvery, err := time.ParseDuration(env("DUDE_DIFF_EVERY", "15s"))
	if err != nil {
		return fmt.Errorf("DUDE_DIFF_EVERY: %w", err)
	}
	// What an hour of a lux host costs, recorded with each Run so its
	// machine time has a price. One rate for every host until lux reports
	// each host's own.
	machineRate, err := strconv.ParseFloat(env("DUDE_MACHINE_USD_PER_HOUR", "0.20"), 64)
	if err != nil || machineRate < 0 {
		return fmt.Errorf("DUDE_MACHINE_USD_PER_HOUR: not a rate: %q", os.Getenv("DUDE_MACHINE_USD_PER_HOUR"))
	}
	host, _ := os.Hostname()

	forges := forge.Resolver{DB: database}
	runtime := workflow.New(database, fmt.Sprintf("orchestrator-%s-%d", host, os.Getpid()), log)
	store := &delivery.Store{DB: database}
	runtime.Register(delivery.Workflow(store, forges))

	signalWorkflow := func(ctx context.Context, org, wf, name string, payload any, key string) error {
		return runtime.Signal(ctx, org, wf, name, payload, key)
	}
	luxClient := lux.New(require("LUX_URL"), require("LUX_API_KEY"))
	syncer := &phases.Syncer{
		DB: database, Lux: luxClient,
		Forges: forges, Agent: agent, Log: log,
		ParkAfter: parkAfter, IdleAfter: idleAfter,
		DiffEvery: diffEvery, MachineUSDPerHour: machineRate,
	}
	defer syncer.Stop()
	pullRequests := &prs.Syncer{DB: database, Forges: forges, Signal: signalWorkflow, Log: log,
		FactoryLogins: list(os.Getenv("DUDE_FACTORY_LOGINS"))}

	var embedder embeddings.Embedder
	if url := os.Getenv("DUDE_EMBEDDINGS_URL"); url != "" {
		dims, err := strconv.Atoi(env("DUDE_EMBEDDINGS_DIMENSIONS", "768"))
		if err != nil || dims != 768 {
			return fmt.Errorf("DUDE_EMBEDDINGS_DIMENSIONS: the index holds 768 dimensions, not %q", os.Getenv("DUDE_EMBEDDINGS_DIMENSIONS"))
		}
		embedder = &embeddings.Client{BaseURL: url, Key: require("DUDE_EMBEDDINGS_KEY"),
			ModelName: env("DUDE_EMBEDDINGS_MODEL", "gemini-embedding-2"), Dims: dims}
		log.Info("memory searches by meaning", "model", embedder.Model(), "url", url)
	} else {
		log.Info("memory searches by words only: DUDE_EMBEDDINGS_URL is not set")
	}
	indexer := &memory.Indexer{DB: database, Embedder: embedder, Log: log}

	notifier := &notify.Notifier{DB: database, Log: log,
		Subject:   env("DUDE_VAPID_SUBJECT", "mailto:dude@localhost"),
		PublicKey: os.Getenv("DUDE_VAPID_PUBLIC_KEY"), PrivateKey: os.Getenv("DUDE_VAPID_PRIVATE_KEY"),
	}

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
		{"artifacts", time.Second, (&phases.Artifacts{DB: database, Lux: luxClient}).Sweep},
		{"webhooks", time.Second, pullRequests.ProcessDeliveries},
		{"notify", 2 * time.Second, notifier.Sweep},
		{"indexer", 5 * time.Second, indexer.Sweep},
		{"pr-reconciler", time.Minute, func(ctx context.Context) (int, error) {
			return pullRequests.Reconcile(ctx, reconcileEvery)
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
		Addr: env("DUDE_ORCHESTRATOR_LISTEN", "127.0.0.1:3100"),
		Handler: (&api.Server{DB: database, Lux: luxClient, Workflow: runtime, Token: require("DUDE_ORCHESTRATOR_TOKEN"), Log: log,
			PushKeys: notifier.Keys, Forges: forges, PRs: pullRequests, Embedder: embedder,
			Kick: func() {
				select {
				case kick <- struct{}{}:
				default:
				}
			}}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	// dude's tools for agents, on a listener of their own: agents reach it
	// from lux's hosts (DUDE_TOOLS_URL is how they see it), so it is not the
	// internal API's loopback.
	var tools *http.Server
	if addr := os.Getenv("DUDE_TOOLS_LISTEN"); addr != "" {
		tools = &http.Server{Addr: addr, Handler: (&agenttools.Server{DB: database, Log: log, Embedder: embedder,
			Kick: func() {
				select {
				case kick <- struct{}{}:
				default:
				}
			}}).Handler(),
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

func require(name string) string {
	v := os.Getenv(name)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", name)
		os.Exit(2)
	}
	return v
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
