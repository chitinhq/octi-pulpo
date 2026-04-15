package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/admission"
	"github.com/chitinhq/octi-pulpo/internal/bootcheck"
	"github.com/chitinhq/octi-pulpo/internal/budget"
	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/learner"
	"github.com/chitinhq/octi-pulpo/internal/mcp"
	"github.com/chitinhq/octi-pulpo/internal/memory"
	"github.com/chitinhq/octi-pulpo/internal/org"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/chitinhq/octi-pulpo/internal/sprint"
	"github.com/chitinhq/octi-pulpo/internal/standup"
	"github.com/redis/go-redis/v9"
)

func main() {
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	namespace := os.Getenv("OCTI_NAMESPACE")
	if namespace == "" {
		namespace = "octi"
	}

	mem, err := memory.New(redisURL, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memory store: %v\n", err)
		os.Exit(1)
	}
	defer mem.Close()

	// Optional: enable vector search via Qdrant + embeddings.
	// Default embedder: Voyage AI (Anthropic's recommended embedding partner).
	// Override with OCTI_EMBEDDINGS_URL for Ollama or other OpenAI-compatible endpoints.
	if qdrantURL := os.Getenv("OCTI_QDRANT_URL"); qdrantURL != "" {
		vc := memory.NewQdrantClient(qdrantURL)
		embURL := os.Getenv("OCTI_EMBEDDINGS_URL")
		embKey := os.Getenv("OCTI_EMBEDDINGS_KEY")     // Voyage AI API key (VOYAGE_API_KEY also checked)
		embModel := os.Getenv("OCTI_EMBEDDINGS_MODEL")
		if embURL == "" {
			embURL = "https://api.voyageai.com"
		}
		if embKey == "" {
			embKey = os.Getenv("VOYAGE_API_KEY")
		}
		if embModel == "" {
			embModel = "voyage-3-lite"
		}
		emb := memory.NewHTTPEmbedder(embURL, embKey, embModel)
		mem = mem.WithVector(vc, emb)
		fmt.Fprintf(os.Stderr, "octi-pulpo: qdrant enabled (%s, embedder: %s/%s)\n", qdrantURL, embURL, embModel)
	}

	coord, err := coordination.New(redisURL, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordination engine: %v\n", err)
		os.Exit(1)
	}
	defer coord.Close()

	healthDir := os.Getenv("CHITIN_HEALTH_DIR")
	router := routing.NewRouter(healthDir) // defaults to ~/.chitin/driver-health/

	// Orphan health sweep: report (and optionally delete) health files for
	// drivers that are no longer in the driverTiers registry. Ladder Forge II
	// (2026-04-14) pruned 8 drivers from the router but left their .json files
	// on disk, where they surfaced in health_report as stale. Default is
	// log-only; set OCTI_PRUNE_ORPHAN_HEALTH=1 to actually delete.
	pruneOrphans := os.Getenv("OCTI_PRUNE_ORPHAN_HEALTH") == "1"
	if orphans, err := router.PruneOrphanHealth(pruneOrphans); err != nil {
		fmt.Fprintf(os.Stderr, "octi-pulpo: orphan health sweep: %v\n", err)
	} else if len(orphans) > 0 {
		if pruneOrphans {
			fmt.Fprintf(os.Stderr, "octi-pulpo: deleted %d orphan health files: %v\n", len(orphans), orphans)
		} else {
			fmt.Fprintf(os.Stderr, "octi-pulpo: WARNING — %d orphan health files in %s: %v (set OCTI_PRUNE_ORPHAN_HEALTH=1 to delete)\n", len(orphans), router.HealthDir(), orphans)
		}
	}

	// Set up the event-driven dispatcher
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse redis url: %v\n", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	home, _ := os.UserHomeDir()
	queueFile := os.Getenv("CHITIN_QUEUE_FILE")
	if queueFile == "" {
		queueFile = filepath.Join(home, ".chitin", "queue.txt")
	}

	eventRouter := dispatch.NewEventRouter(dispatch.DefaultRules())
	dispatcher := dispatch.NewDispatcher(rdb, router, coord, eventRouter, queueFile, namespace)

	// Set up adaptive cooldown profiles with live driver-health signal.
	profiles := dispatch.NewProfileStore(rdb, namespace, eventRouter.CooldownFor)
	profiles.SetBudgetHealthFn(func() float64 {
		health := router.HealthReport()
		if len(health) == 0 {
			return 1.0 // no drivers discovered — assume healthy
		}
		var closed int
		for _, h := range health {
			if h.CircuitState != "OPEN" {
				closed++
			}
		}
		return float64(closed) / float64(len(health))
	})
	dispatcher.SetProfiles(profiles)

	// Set up sprint store
	sprintStore := sprint.NewStore(rdb, namespace)

	// Set up benchmark tracker
	benchmark := dispatch.NewBenchmarkTracker(rdb, namespace)

	// Set up org store
	orgStore := org.NewOrgStore(rdb, namespace)

	// Set up budget store
	budgetStore := budget.NewBudgetStore(rdb, namespace)
	dispatcher.SetBudget(budgetStore)

	// Set up goal store
	goalStore := sprint.NewGoalStore(rdb, namespace)

	// Set up standup store
	standupStore := standup.New(rdb, namespace)

	server := mcp.New(mem, coord, router)
	server.SetDispatcher(dispatcher)
	server.SetSprintStore(sprintStore)
	server.SetStandupStore(standupStore)
	server.SetBenchmark(benchmark)
	server.SetOrgStore(orgStore)
	server.SetBudgetStore(budgetStore)
	server.SetGoalStore(goalStore)
	server.SetProfileStore(profiles)
	server.SetRedis(rdb, namespace)
	server.SetAdmissionGate(admission.New(rdb, namespace))

	// Initialize API-driven dispatch adapters
	anthropicAdapter := dispatch.NewAnthropicAdapter("", "")
	ghActionsAdapter := dispatch.NewGHActionsAdapter("")
	copilotAdapter := dispatch.NewCopilotAdapter("")
	openclawAdapter := dispatch.NewOpenClawAdapter("", "", "", "")

	// Wire learner for auto-store of task outcomes in episodic memory.
	taskLearner := learner.New(mem)
	anthropicAdapter.SetLearner(taskLearner)
	copilotAdapter.SetLearner(taskLearner)
	openclawAdapter.SetLearner(taskLearner)

	server.SetAnthropicAdapter(anthropicAdapter)
	server.SetGHActionsAdapter(ghActionsAdapter)
	server.SetCopilotAdapter(copilotAdapter)

	// Boot-time telemetry self-audit (workspace#408 / Telemetry Truth).
	// Never hard-fails boot — a crash-loop is just a louder lie.
	bcCache := bootcheck.NewCache()
	{
		bcCtx, cancelBC := context.WithTimeout(context.Background(), 10*time.Second)
		bcRep := bootcheck.Run(bcCtx, bootcheck.Deps{
			RDB:         rdb,
			Namespace:   namespace,
			Router:      router,
			Benchmark:   benchmark,
			Profiles:    profiles,
			GitHubToken: os.Getenv("GITHUB_TOKEN"),
		})
		cancelBC()
		bcRep.Render(os.Stderr)
		bcCache.Set(bcRep)
	}
	server.SetBootcheckCache(bcCache)

	// Optional HTTP mode: run webhook server alongside MCP
	httpPort := os.Getenv("OCTI_HTTP_PORT")
	if httpPort != "" {
		secretFile := os.Getenv("CHITIN_WEBHOOK_SECRET_FILE")
		ws := dispatch.NewWebhookServer(dispatcher, secretFile)
		ws.SetSprintStore(sprintStore)
		ws.SetBenchmark(benchmark)
		ws.SetBudgetStore(budgetStore)
		ws.SetMemoryStore(mem)

		// Wire triage handler — Claude API calls stay on this box
		triageHandler := dispatch.NewTriageHandler("", "", "")
		triageHandler.SetBudgetStore(budgetStore)
		ws.SetTriageHandler(triageHandler)

		// Wire review handler — reviews + approves + merges via Claude API locally
		reviewHandler := dispatch.NewReviewHandler("", "", "")
		reviewHandler.SetBudgetStore(budgetStore)
		ws.SetReviewHandler(reviewHandler)

		// Wire planner handler — scopes vague issues via Claude API locally
		plannerHandler := dispatch.NewPlannerHandler("", "", "")
		plannerHandler.SetBudgetStore(budgetStore)
		ws.SetPlannerHandler(plannerHandler)

		// Wire coding handler — Tier B senior coding for escalated PRs
		codingHandler := dispatch.NewCodingHandler("", "", "")
		codingHandler.SetBudgetStore(budgetStore)
		ws.SetCodingHandler(codingHandler)

		// Wire cascade handler — syncs roadmap to issues across repos
		cascadeHandler := dispatch.NewCascadeHandler("", "", "")
		ws.SetCascadeHandler(cascadeHandler)

		// Wire Slack Events API command handler when credentials are set.
		if slackSecret := os.Getenv("SLACK_SIGNING_SECRET"); slackSecret != "" {
			slackBotToken := os.Getenv("SLACK_BOT_TOKEN")
			evHandler := dispatch.NewSlackEventHandler(slackSecret, slackBotToken, dispatcher)
			evHandler.SetSprintStore(sprintStore)
			evHandler.SetBenchmark(benchmark)
			evHandler.SetBudgetStore(budgetStore)
			if slackURL := os.Getenv("SLACK_WEBHOOK_URL"); slackURL != "" {
				evHandler.SetNotifier(dispatch.NewNotifier(slackURL))
			}
			ws.SetSlackEvents(evHandler)
			fmt.Fprintf(os.Stderr, "octi-pulpo: slack events handler registered on /slack/events\n")
		}

		// Daemon mode: if OCTI_DAEMON=1 or stdin is not a terminal, run HTTP only (no MCP stdio)
		daemon := os.Getenv("OCTI_DAEMON") == "1"
		if !daemon {
			if fi, err := os.Stdin.Stat(); err == nil {
				daemon = fi.Mode()&os.ModeCharDevice == 0 && fi.Size() == 0
			}
		}

		if daemon {
			addr := ":" + httpPort
			fmt.Fprintf(os.Stderr, "octi-pulpo daemon: webhook server on %s, redis %s\n", addr, redisURL)

			// Start signal watcher — reacts to agent signals via Redis pub/sub
			sw := dispatch.NewSignalWatcher(dispatcher, rdb, namespace)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				if err := sw.Watch(ctx); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "signal watcher: %v\n", err)
				}
			}()

			// Start brain — periodic intelligence loop with sprint + profile awareness
			chains := dispatch.DefaultChains()
			brain := dispatch.NewBrain(dispatcher, chains)
			brain.SetSprintStore(sprintStore)
			brain.SetProfileStore(profiles)
			brain.SetStandupStore(standupStore)
			// Wire task adapters: Clawta → GH Actions (Copilot) → Copilot SDK → OpenClaw (Matrix)
			clawtaBinary := filepath.Join(home, "workspace", "clawta", "bench", "clawta-linux-amd64")
			clawtaAdapter := dispatch.NewClawtaAdapter(clawtaBinary, "", "", "")
			clawtaAdapter.SetLearner(taskLearner)
			brain.SetAdapters(clawtaAdapter, ghActionsAdapter, copilotAdapter, openclawAdapter)
			// T1 local enablement (turing, 2026-04-15): also register adapters on
			// the Dispatcher itself so non-brain dispatch paths (timer, webhook,
			// signal-watcher) invoke a real execution surface for the routed
			// driver rather than falling through to the legacy queue-only path
			// that logs Action="dispatched" with "no adapter registered" reason.
			// Without this, router.Recommend() can pick clawta (tier=local) but
			// dispatcher.selectAdapter() returns nil and nothing actually runs.
			// See chitinhq/octi#243 (silent-loss) and the T1 local=0 tracker.
			dispatcher.SetAdapters(clawtaAdapter, ghActionsAdapter, copilotAdapter, openclawAdapter, anthropicAdapter)
			if ghToken := os.Getenv("GITHUB_TOKEN"); ghToken != "" {
				brain.SetGitHubToken(ghToken)
			}
			// Wire ntfy push notifier for brain alerts (topic: ganglia).
			ntfyBase := os.Getenv("NTFY_BASE_URL")
			if ntfyBase == "" {
				ntfyBase = "https://ntfy.sh"
			}
			ntfyTopic := os.Getenv("NTFY_TOPIC")
			if ntfyTopic == "" {
				ntfyTopic = "ganglia"
			}
			brain.SetNotifier(dispatch.NewNtfyNotifier(ntfyBase, ntfyTopic))
			// Wire swarm CLI adapters
			modelRouter := dispatch.NewModelRouter()
			staggerTracker := dispatch.NewStaggerTracker(rdb, namespace)

			// Load platform config for config-driven dispatch.
			platformConfigPath := filepath.Join(home, "workspace", "octi", "server", "platforms.json")
			platformCfgHolder, err := dispatch.NewPlatformConfigHolder(platformConfigPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "platform config: %v (falling back to legacy dispatch)\n", err)
			} else {
				brain.SetPlatformConfig(platformCfgHolder)
				// Register platforms in stagger tracker from config.
				pcfg := platformCfgHolder.Get()
				for _, name := range pcfg.Priority {
					entry := pcfg.Platforms[name]
					cooldown := 30 * time.Minute // default
					if name == "claude" {
						cooldown = 45 * time.Minute
					}
					staggerTracker.RegisterPlatform(name, cooldown, entry.DailyCap)
				}
			}

			skipList := dispatch.NewSkipList(rdb, namespace)
			if n := skipList.LoadFromRedis(); n > 0 {
				fmt.Fprintf(os.Stderr, "octi-pulpo: loaded %d skip-list entries from Redis\n", n)
			}
			brain.SetSkipList(skipList)

			escalationMgr := dispatch.NewEscalationManager(modelRouter)
			queueMachine := dispatch.NewQueueMachine()
			brain.SetModelRouter(modelRouter)
			brain.SetStagger(staggerTracker)
			brain.SetEscalationManager(escalationMgr)
			brain.SetQueueMachine(queueMachine)
			// Give the Slack events handler access to the brain for constraint queries.
			if ws.SlackEvents() != nil {
				ws.SlackEvents().SetBrain(brain)
			}

			// Hot-reload platform config on SIGHUP.
			go func() {
				sigCh := make(chan os.Signal, 1)
				signal.Notify(sigCh, syscall.SIGHUP)
				for range sigCh {
					if platformCfgHolder != nil {
						if err := platformCfgHolder.Reload(); err != nil {
							fmt.Fprintf(os.Stderr, "platform config reload: %v\n", err)
						} else {
							fmt.Fprintf(os.Stderr, "platform config reloaded\n")
						}
					}
				}
			}()

			go func() {
				if err := brain.Run(ctx); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "brain: %v\n", err)
				}
			}()

			fmt.Fprintf(os.Stderr, "octi-pulpo daemon: signal watcher + brain + sprint store + benchmarks started\n")

			if err := ws.ListenAndServe(addr); err != nil {
				fmt.Fprintf(os.Stderr, "webhook server: %v\n", err)
				os.Exit(1)
			}
			return
		}

		go func() {
			addr := ":" + httpPort
			fmt.Fprintf(os.Stderr, "webhook server listening on %s\n", addr)
			if err := ws.ListenAndServe(addr); err != nil {
				fmt.Fprintf(os.Stderr, "webhook server: %v\n", err)
			}
		}()
	}

	if err := server.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}
