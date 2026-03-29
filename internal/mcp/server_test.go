package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

// --- enrichHealthReport ---

func TestEnrichHealthReport_HealthyDriver(t *testing.T) {
	recent := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	drivers := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", Failures: 0, LastSuccess: recent},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "claude-code" {
		t.Errorf("Name: got %q, want claude-code", e.Name)
	}
	if e.CircuitState != "CLOSED" {
		t.Errorf("CircuitState: got %q, want CLOSED", e.CircuitState)
	}
	if !strings.HasSuffix(e.Recommendation, ": healthy") {
		t.Errorf("Recommendation: got %q, want healthy suffix", e.Recommendation)
	}
	if e.SecsSinceSuccess <= 0 {
		t.Errorf("SecsSinceSuccess should be positive, got %d", e.SecsSinceSuccess)
	}
}

func TestEnrichHealthReport_OpenCircuit(t *testing.T) {
	drivers := []routing.DriverHealth{
		{Name: "goose", CircuitState: "OPEN", Failures: 5},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.Recommendation, "budget exhausted") {
		t.Errorf("Recommendation for OPEN: got %q, want 'budget exhausted'", e.Recommendation)
	}
}

func TestEnrichHealthReport_HalfOpenCircuit(t *testing.T) {
	drivers := []routing.DriverHealth{
		{Name: "copilot", CircuitState: "HALF", Failures: 2},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.Recommendation, "recovering") {
		t.Errorf("Recommendation for HALF: got %q, want 'recovering'", e.Recommendation)
	}
}

func TestEnrichHealthReport_StaleSuccess(t *testing.T) {
	// Success was 2 hours ago — should trigger the stale warning.
	stale := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	drivers := []routing.DriverHealth{
		{Name: "gemini", CircuitState: "CLOSED", LastSuccess: stale},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if !strings.Contains(e.Recommendation, "healthy but no success") {
		t.Errorf("Recommendation for stale: got %q, want 'healthy but no success'", e.Recommendation)
	}
}

func TestEnrichHealthReport_EmptyList(t *testing.T) {
	entries := enrichHealthReport(nil)
	if len(entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(entries))
	}
}

func TestEnrichHealthReport_NoLastSuccess(t *testing.T) {
	drivers := []routing.DriverHealth{
		{Name: "new-driver", CircuitState: "CLOSED"},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	// SecsSinceSuccess should be zero when LastSuccess is empty.
	if entries[0].SecsSinceSuccess != 0 {
		t.Errorf("SecsSinceSuccess should be 0 when LastSuccess empty, got %d", entries[0].SecsSinceSuccess)
	}
}

func TestEnrichHealthReport_MultipleDrivers(t *testing.T) {
	recent := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	drivers := []routing.DriverHealth{
		{Name: "driver-a", CircuitState: "CLOSED", LastSuccess: recent},
		{Name: "driver-b", CircuitState: "OPEN"},
		{Name: "driver-c", CircuitState: "HALF"},
	}

	entries := enrichHealthReport(drivers)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Name != "driver-a" || entries[1].Name != "driver-b" || entries[2].Name != "driver-c" {
		t.Error("entries not in original order")
	}
}

// --- textResult ---

func TestTextResult_Structure(t *testing.T) {
	resp := textResult(42, "hello world")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC: got %q, want 2.0", resp.JSONRPC)
	}
	if resp.ID != 42 {
		t.Errorf("ID: got %v, want 42", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("Error should be nil, got %v", resp.Error)
	}
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok {
		t.Fatal("content[\"content\"] should be []map[string]string")
	}
	if len(items) != 1 || items[0]["text"] != "hello world" {
		t.Errorf("unexpected text content: %v", items)
	}
}

// --- errorResp ---

func TestErrorResp_Structure(t *testing.T) {
	resp := errorResp("req-1", -32600, "invalid request")
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC: got %q, want 2.0", resp.JSONRPC)
	}
	if resp.ID != "req-1" {
		t.Errorf("ID: got %v, want req-1", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("Error should not be nil")
	}
	if resp.Error.Code != -32600 {
		t.Errorf("Error.Code: got %d, want -32600", resp.Error.Code)
	}
	if resp.Error.Message != "invalid request" {
		t.Errorf("Error.Message: got %q, want 'invalid request'", resp.Error.Message)
	}
	if resp.Result != nil {
		t.Errorf("Result should be nil on error response, got %v", resp.Result)
	}
}

// --- toolDefs ---

func TestToolDefs_ContainsExpectedTools(t *testing.T) {
	expected := []string{
		"memory_store",
		"memory_recall",
		"memory_status",
		"coord_claim",
		"coord_signal",
		"route_recommend",
		"health_report",
		"dispatch_event",
		"dispatch_status",
		"dispatch_trigger",
		"sprint_status",
		"sprint_sync",
		"benchmark_status",
		"agent_leaderboard",
	}

	defs := toolDefs()
	nameSet := make(map[string]bool, len(defs))
	for _, d := range defs {
		nameSet[d.Name] = true
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		if d.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", d.Name)
		}
	}

	for _, name := range expected {
		if !nameSet[name] {
			t.Errorf("expected tool %q not found in toolDefs()", name)
		}
	}
}

func TestToolDefs_NoDuplicateNames(t *testing.T) {
	defs := toolDefs()
	seen := make(map[string]int)
	for _, d := range defs {
		seen[d.Name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("tool %q appears %d times in toolDefs()", name, count)
		}
	}
}
