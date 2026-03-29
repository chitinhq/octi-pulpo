// octi-worker: Redis-native worker that dequeues from Octi Pulpo's priority queue
// and executes agents via run-agent.sh.
//
// Replaces the 32 bash workers polling queue.txt with direct Redis consumption.
//
// Usage:
//
//	OCTI_REDIS_URL=redis://localhost:6379 OCTI_WORKERS=32 octi-worker
//
// Each worker goroutine:
//  1. Calls Dispatcher.Dequeue() to get highest-priority agent
//  2. Spawns run-agent.sh <agent-name> as subprocess
//  3. Waits for completion, records result
//  4. Releases the coordination claim
//  5. Loops
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/redis/go-redis/v9"
)

func main() {
	redisURL := envOr("OCTI_REDIS_URL", "redis://localhost:6379")
	namespace := envOr("OCTI_NAMESPACE", "octi")
	workerCount := envInt("OCTI_WORKERS", 32)
	workspaceDir := envOr("WORKSPACE_DIR", filepath.Join(os.Getenv("HOME"), "agentguard-workspace"))
	runAgentScript := envOr("OCTI_RUN_AGENT", filepath.Join(workspaceDir, "server", "run-agent.sh"))
	pollInterval := 5 * time.Second

	// Validate run-agent.sh exists
	if _, err := os.Stat(runAgentScript); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "run-agent.sh not found at %s\n", runAgentScript)
		os.Exit(1)
	}

	// Set up Redis + dispatcher (we only use Dequeue + ReleaseClaim + RecordWorkerResult)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse redis url: %v\n", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	// Verify Redis is reachable
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintf(os.Stderr, "redis ping failed: %v\n", err)
		os.Exit(1)
	}

	coord, err := coordination.New(redisURL, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coordination engine: %v\n", err)
		os.Exit(1)
	}
	defer coord.Close()

	healthDir := os.Getenv("AGENTGUARD_HEALTH_DIR")
	router := routing.NewRouter(healthDir)
	eventRouter := dispatch.NewEventRouter(dispatch.DefaultRules())
	dispatcher := dispatch.NewDispatcher(rdb, router, coord, eventRouter, "", namespace)

	// Load completion chains for reactive dispatch
	chains := dispatch.DefaultChains()
	fmt.Fprintf(os.Stderr, "octi-worker: loaded %d completion chains\n", len(chains))

	fmt.Fprintf(os.Stderr, "octi-worker: starting %d workers, redis %s, namespace %s\n", workerCount, redisURL, namespace)
	fmt.Fprintf(os.Stderr, "octi-worker: run-agent.sh at %s\n", runAgentScript)

	// Graceful shutdown
	shutdownCtx, cancel := context.WithCancel(ctx)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		fmt.Fprintf(os.Stderr, "octi-worker: received %s, shutting down (finishing current agents)...\n", sig)
		cancel()
	}()

	// Launch worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			workerLoop(shutdownCtx, dispatcher, runAgentScript, workerID, pollInterval, workspaceDir, chains)
		}(i)
	}

	wg.Wait()
	fmt.Fprintf(os.Stderr, "octi-worker: all workers stopped\n")
}

func workerLoop(ctx context.Context, d *dispatch.Dispatcher, script string, id int, pollInterval time.Duration, workspaceDir string, chains dispatch.ChainConfig) {
	for {
		// Check for shutdown
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Dequeue highest-priority agent
		agent, err := d.Dequeue(ctx)
		if err != nil {
			// Context cancelled during dequeue is expected on shutdown
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(os.Stderr, "worker[%d]: dequeue error: %v\n", id, err)
			sleep(ctx, pollInterval)
			continue
		}

		if agent == "" {
			// Queue empty — sleep and retry
			sleep(ctx, pollInterval)
			continue
		}

		fmt.Fprintf(os.Stderr, "worker[%d]: executing %s\n", id, agent)
		start := time.Now()

		exitCode := executeAgent(ctx, script, agent)
		duration := time.Since(start).Seconds()

		if exitCode == 0 {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s completed (%.1fs)\n", id, agent, duration)
		} else {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s failed exit=%d (%.1fs)\n", id, agent, exitCode, duration)
		}

		// Release the coordination claim so the agent can be dispatched again
		releaseCtx := context.Background() // don't use shutdown ctx for cleanup
		if err := d.ReleaseClaim(releaseCtx, agent); err != nil {
			fmt.Fprintf(os.Stderr, "worker[%d]: release claim error for %s: %v\n", id, agent, err)
		}

		// Record result for observability
		d.RecordWorkerResult(releaseCtx, agent, exitCode, duration)

		// Trigger completion chains — dispatch follow-up agents based on result
		madeCommits := dispatch.CheckForCommits(agent, workspaceDir)
		if madeCommits {
			fmt.Fprintf(os.Stderr, "worker[%d]: %s made commits, checking chains\n", id, agent)
		}
		chainResults := dispatch.TriggerChains(releaseCtx, d, chains, agent, exitCode, madeCommits)
		for _, cr := range chainResults {
			fmt.Fprintf(os.Stderr, "worker[%d]: chain %s -> %s (%s: %s)\n", id, agent, cr.Agent, cr.Action, cr.Reason)
		}
	}
}

func executeAgent(ctx context.Context, script, agent string) int {
	cmd := exec.CommandContext(ctx, "bash", script, agent)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		// If context was cancelled, the process was killed — return special code
		if ctx.Err() != nil {
			return -1
		}
		return 1
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
