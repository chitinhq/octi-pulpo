package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/mcp"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
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

	server := mcp.New(mem, coord, router)
	server.SetDispatcher(dispatcher)

	// Optional HTTP mode: run webhook server alongside MCP
	httpPort := os.Getenv("OCTI_HTTP_PORT")
	if httpPort != "" {
		secretFile := os.Getenv("AGENTGUARD_WEBHOOK_SECRET_FILE")
		ws := dispatch.NewWebhookServer(dispatcher, secretFile)
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
