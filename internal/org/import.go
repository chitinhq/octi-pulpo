package org

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------- schedule.json types ----------

type scheduleJSON struct {
	Agents map[string]scheduleAgent `json:"agents"`
}

type scheduleAgent struct {
	Driver  string `json:"driver"`
	Cron    string `json:"cron"`
	Repo    string `json:"repo"`
	Box     string `json:"box"`
	Squad   string `json:"squad"`
	Enabled bool   `json:"enabled"`
}

// ---------- squad state types ----------

type squadState struct {
	Squad  string                      `json:"squad"`
	Agents map[string]squadAgentState  `json:"agents"`
}

type squadAgentState struct {
	Role   string `json:"role"`
	Status string `json:"status"`
}

// ---------- import logic ----------

// ImportFromSchedule reads a schedule.json and squad state files, then
// populates the OrgStore with all enabled agents plus "jared" as the board
// root. Returns the number of agents stored.
func ImportFromSchedule(ctx context.Context, store *OrgStore, schedulePath, squadsDir string) (int, error) {
	raw, err := os.ReadFile(schedulePath)
	if err != nil {
		return 0, fmt.Errorf("org import: read schedule: %w", err)
	}
	var sched scheduleJSON
	if err := json.Unmarshal(raw, &sched); err != nil {
		return 0, fmt.Errorf("org import: parse schedule: %w", err)
	}

	// Load squad state files for role lookups.
	squadRoles := loadSquadRoles(squadsDir)

	// Always add "jared" as board root.
	count := 0
	jared := Agent{Name: "jared", Role: "Board"}
	if err := store.Put(ctx, jared); err != nil {
		return 0, fmt.Errorf("org import: put jared: %w", err)
	}
	count++

	for name, sa := range sched.Agents {
		if !sa.Enabled {
			continue
		}

		role := inferRole(name)
		// Override with squad state role if available.
		if sr, ok := squadRoles[name]; ok && sr != "" {
			role = sr
		}

		agent := Agent{
			Name:      name,
			Squad:     sa.Squad,
			Role:      role,
			ReportsTo: inferReportsTo(name, role, sa.Squad),
			Box:       sa.Box,
			Driver:    sa.Driver,
		}
		if err := store.Put(ctx, agent); err != nil {
			return count, fmt.Errorf("org import: put %q: %w", name, err)
		}
		count++
	}

	return count, nil
}

// loadSquadRoles reads all JSON files in squadsDir and builds a map of
// agent name -> role from the squad state files.
func loadSquadRoles(squadsDir string) map[string]string {
	roles := make(map[string]string)
	if squadsDir == "" {
		return roles
	}

	entries, err := os.ReadDir(squadsDir)
	if err != nil {
		return roles
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(squadsDir, e.Name()))
		if err != nil {
			continue
		}
		var state squadState
		if err := json.Unmarshal(raw, &state); err != nil {
			continue
		}
		for agentName, as := range state.Agents {
			if as.Role != "" {
				roles[agentName] = as.Role
			}
		}
	}
	return roles
}

// inferRole derives a role from the agent name suffix.
func inferRole(name string) string {
	switch {
	case strings.HasSuffix(name, "-em"):
		return "EM"
	case strings.HasSuffix(name, "-sr"):
		return "SR"
	case strings.HasSuffix(name, "-jr"):
		return "JR"
	case strings.HasSuffix(name, "-qa"):
		return "QA"
	case strings.HasSuffix(name, "-pl"):
		return "PL"
	case strings.HasSuffix(name, "-arch"):
		return "Arch"
	case strings.Contains(name, "director"):
		return "Director"
	default:
		return ""
	}
}

// inferReportsTo determines who an agent reports to based on role and squad.
func inferReportsTo(name, role, squad string) string {
	switch role {
	case "Director":
		return "jared"
	case "EM":
		return "director"
	default:
		if squad != "" {
			return squad + "-em"
		}
		return ""
	}
}

// ---------- tree printing ----------

// PrintTree renders the org chart as indented text starting from "jared".
func PrintTree(ctx context.Context, store *OrgStore) (string, error) {
	agents, err := store.All(ctx)
	if err != nil {
		return "", fmt.Errorf("org tree: %w", err)
	}

	// Build children map.
	children := make(map[string][]string)
	for _, a := range agents {
		if a.ReportsTo != "" {
			children[a.ReportsTo] = append(children[a.ReportsTo], a.Name)
		}
	}
	// Sort children for deterministic output.
	for k := range children {
		sort.Strings(children[k])
	}

	var b strings.Builder
	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		// Find the agent to get role/squad info.
		label := name
		for _, a := range agents {
			if a.Name == name {
				parts := []string{name}
				if a.Role != "" {
					parts = append(parts, "["+a.Role+"]")
				}
				if a.Squad != "" {
					parts = append(parts, "("+a.Squad+")")
				}
				label = strings.Join(parts, " ")
				break
			}
		}
		fmt.Fprintf(&b, "%s%s\n", strings.Repeat("  ", depth), label)
		for _, child := range children[name] {
			walk(child, depth+1)
		}
	}

	walk("jared", 0)
	return b.String(), nil
}
