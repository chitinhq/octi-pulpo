package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AgentGuardHQ/octi-pulpo/internal/admission"
	"github.com/AgentGuardHQ/octi-pulpo/internal/budget"
	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/mcp"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
	"github.com/AgentGuardHQ/octi-pulpo/internal/org"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
	"github.com/AgentGuardHQ/octi-pulpo/internal/standup"
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

	coord, err := coordination.New(redisURL, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordination engine: %v\n", err)
		os.Exit(1)
	}
	defer coord.Close()

	healthDir := os.Getenv("AGENTGUARD_HEALTH_DIR")
	router := routing.NewRouter(healthDir) // defaults to ~/.agentguard/driver-health/

	// Set up the event-driven dispatcher
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse redis url: %v\n", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	home, _ := os.UserHomeDir()
	queueFile := os.Getenv("AGENTGUARD_QUEUE_FILE")
	if queueFile == "" {
		queueFile = filepath.Join(home, ".agentguard", "queue.txt")
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

	// Optional HTTP mode: run webhook server alongside MCP
	httpPort := os.Getenv("OCTI_HTTP_PORT")
	if httpPort != "" {
		secretFile := os.Getenv("AGENTGUARD_WEBHOOK_SECRET_FILE")
		ws := dispatch.NewWebhookServer(dispatcher, secretFile)
		ws.SetSprintStore(sprintStore)
		ws.SetBenchmark(benchmark)
		ws.SetBudgetStore(budgetStore)

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
			if slackURL := os.Getenv("SLACK_WEBHOOK_URL"); slackURL != "" {
				brain.SetNotifier(dispatch.NewNotifier(slackURL))
			}
			// Give the Slack events handler access to the brain for constraint queries.
			if ws.SlackEvents() != nil {
				ws.SlackEvents().SetBrain(brain)
			}
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
