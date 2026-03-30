package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

// newTestServer creates a minimal Server with only a router wired (no Redis).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	router := routing.NewRouterWithTiers(dir, map[string]routing.CostTier{})
	return New(nil, nil, router)
}

// newTestServerNoRouter creates a Server with all stores nil.
func newTestServerNoRouter() *Server {
	return &Server{}
}

func makeRequest(id interface{}, method string, params interface{}) Request {
	raw, _ := json.Marshal(params)
	return Request{JSONRPC: "2.0", ID: id, Method: method, Params: json.RawMessage(raw)}
}

// --- handle: protocol-level methods ---

func TestHandle_Initialize(t *testing.T) {
	s := newTestServer(t)
	req := makeRequest(1, "initialize", map[string]interface{}{})
	resp := s.handle(req)

	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC: got %q, want 2.0", resp.JSONRPC)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion: got %v, want 2024-11-05", result["protocolVersion"])
	}
	serverInfo, ok := result["serverInfo"].(map[string]string)
	if !ok {
		t.Fatal("serverInfo should be map[string]string")
	}
	if serverInfo["name"] != "octi-pulpo" {
		t.Errorf("serverInfo.name: got %q, want octi-pulpo", serverInfo["name"])
	}
}

func TestHandle_ToolsList(t *testing.T) {
	s := newTestServer(t)
	req := makeRequest(2, "tools/list", map[string]interface{}{})
	resp := s.handle(req)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	tools, ok := result["tools"]
	if !ok {
		t.Fatal("result missing 'tools' key")
	}
	defs, ok := tools.([]ToolDef)
	if !ok {
		t.Fatalf("tools should be []ToolDef, got %T", tools)
	}
	if len(defs) == 0 {
		t.Error("expected non-empty tool list")
	}
}

func TestHandle_UnknownMethod(t *testing.T) {
	s := newTestServer(t)
	req := makeRequest(3, "notifications/something", nil)
	resp := s.handle(req)

	if resp.Error != nil {
		t.Errorf("unexpected error on unknown method: %v", resp.Error)
	}
	if resp.Result == nil {
		t.Error("expected non-nil empty result for unknown method")
	}
}

// --- handleToolCall: invalid params ---

func TestHandleToolCall_InvalidParams(t *testing.T) {
	s := newTestServer(t)
	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  json.RawMessage(`{not valid json`),
	}
	resp := s.handle(req)
	if resp.Error == nil {
		t.Error("expected error for invalid params JSON, got nil")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code: got %d, want -32602", resp.Error.Code)
	}
}

func TestHandleToolCall_UnknownTool(t *testing.T) {
	s := newTestServer(t)
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "no_such_tool",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Error("expected error for unknown tool, got nil")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("error code: got %d, want -32601", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "no_such_tool") {
		t.Errorf("error message should mention tool name, got %q", resp.Error.Message)
	}
}

// --- handleToolCall: nil-store guard paths ---

func TestHandleToolCall_DispatchEvent_NilDispatcher(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "dispatch_event",
		"arguments": map[string]interface{}{"eventType": "pr_opened"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	if !strings.Contains(resp.Error.Message, "dispatcher not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_DispatchStatus_NilDispatcher(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "dispatch_status",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	if !strings.Contains(resp.Error.Message, "dispatcher not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_DispatchTrigger_NilDispatcher(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "dispatch_trigger",
		"arguments": map[string]interface{}{"agent": "kernel-sr"},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when dispatcher is nil")
	}
	if !strings.Contains(resp.Error.Message, "dispatcher not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintStatus_NilSprintStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "sprint_status",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when sprint store is nil")
	}
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_SprintSync_NilSprintStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "sprint_sync",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when sprint store is nil")
	}
	if !strings.Contains(resp.Error.Message, "sprint store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_BenchmarkStatus_NilBenchmark(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "benchmark_status",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when benchmark is nil")
	}
	if !strings.Contains(resp.Error.Message, "benchmark tracker not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

func TestHandleToolCall_AgentLeaderboard_NilProfileStore(t *testing.T) {
	s := newTestServerNoRouter()
	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "agent_leaderboard",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error == nil {
		t.Fatal("expected error when profile store is nil")
	}
	if !strings.Contains(resp.Error.Message, "profile store not initialized") {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// --- handleToolCall: route_recommend (no Redis needed) ---

func TestHandleToolCall_RouteRecommend(t *testing.T) {
	dir := t.TempDir()
	// Write a health file for a known driver so Recommend has something to read.
	healthData := `{"name":"claude-code","circuit_state":"CLOSED","failures":0}`
	os.WriteFile(filepath.Join(dir, "claude-code.json"), []byte(healthData), 0o644)

	router := routing.NewRouterWithTiers(dir, map[string]routing.CostTier{
		"claude-code": routing.TierSubscription,
	})
	s := New(nil, nil, router)

	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name": "route_recommend",
		"arguments": map[string]interface{}{
			"taskDescription": "write a unit test",
			"budget":          "medium",
		},
	})
	resp := s.handle(req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	// Result should contain a JSON object with a "driver" field.
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok || len(items) == 0 {
		t.Fatal("expected content items")
	}
	text := items[0]["text"]
	if !strings.Contains(text, "driver") {
		t.Errorf("route_recommend result should contain 'driver', got: %s", text)
	}
}

// --- handleToolCall: health_report (no Redis needed) ---

func TestHandleToolCall_HealthReport(t *testing.T) {
	dir := t.TempDir()
	healthData := `{"name":"goose","circuit_state":"OPEN","failures":5}`
	os.WriteFile(filepath.Join(dir, "goose.json"), []byte(healthData), 0o644)

	router := routing.NewRouterWithTiers(dir, map[string]routing.CostTier{
		"goose": routing.TierCLI,
	})
	s := New(nil, nil, router)

	req := makeRequest(1, "tools/call", map[string]interface{}{
		"name":      "health_report",
		"arguments": map[string]interface{}{},
	})
	resp := s.handle(req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	content, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result should be a map")
	}
	items, ok := content["content"].([]map[string]string)
	if !ok || len(items) == 0 {
		t.Fatal("expected content items")
	}
	// Should be a JSON array of health entries.
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(items[0]["text"]), &entries); err != nil {
		t.Fatalf("health_report result is not JSON array: %v\ntext: %s", err, items[0]["text"])
	}
}
