package main

import (
	"fmt"
	"os"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/mcp"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
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

	server := mcp.New(mem, coord)
	if err := server.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}
