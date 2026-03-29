package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

// ToolDef describes an MCP tool for the ListTools response.
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// Request is a JSON-RPC 2.0 request from the MCP client.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is the Octi Pulpo MCP server.
type Server struct {
	mem        *memory.Store
	coord      *coordination.Engine
	router     *routing.Router
	dispatcher *dispatch.Dispatcher
}

// New creates an MCP server backed by the given memory and coordination engines.
func New(mem *memory.Store, coord *coordination.Engine, router *routing.Router) *Server {
	return &Server{mem: mem, coord: coord, router: router}
}

// SetDispatcher adds dispatch capabilities to the MCP server.
func (s *Server) SetDispatcher(d *dispatch.Dispatcher) {
	s.dispatcher = d
}

// Serve runs the MCP server on stdio (stdin/stdout JSON-RPC).
func (s *Server) Serve() error {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode request: %w", err)
		}

		resp := s.handle(req)
		if err := encoder.Encode(resp); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}
}

func (s *Server) handle(req Request) Response {
	switch req.Method {
	case "initialize":
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "octi-pulpo", "version": "0.1.0"},
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		}}
	case "tools/list":
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"tools": toolDefs()}}
	case "tools/call":
		return s.handleToolCall(req)
	default:
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
	}
}

func (s *Server) handleToolCall(req Request) Response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResp(req.ID, -32602, "invalid params")
	}

	ctx := context.Background()
	agentID := os.Getenv("AGENTGUARD_AGENT_NAME")
	if agentID == "" {
		agentID = "unknown"
	}

	switch params.Name {
	case "memory_store":
		var args struct {
			Content string   `json:"content"`
			Topics  []string `json:"topics"`
		}
		json.Unmarshal(params.Arguments, &args)
		id, err := s.mem.Put(ctx, agentID, args.Content, args.Topics)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Stored memory %s (topics: %s)", id, strings.Join(args.Topics, ", ")))

	case "memory_recall":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Limit == 0 {
			args.Limit = 5
		}
		results, err := s.mem.Recall(ctx, args.Query, args.Limit)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(results) == 0 {
			return textResult(req.ID, "No relevant memories found.")
		}
		var lines []string
		for i, m := range results {
			lines = append(lines, fmt.Sprintf("%d. [%s] %s (topics: %s)", i+1, m.AgentID, m.Content, strings.Join(m.Topics, ", ")))
		}
		return textResult(req.ID, strings.Join(lines, "\n"))

	case "memory_status":
		claims, err := s.coord.ActiveClaims(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(claims) == 0 {
			return textResult(req.ID, "No agents have active claims right now.")
		}
		var lines []string
		for _, c := range claims {
			lines = append(lines, fmt.Sprintf("- %s: %s (claimed %s)", c.AgentID, c.Task, c.ClaimedAt))
		}
		return textResult(req.ID, strings.Join(lines, "\n"))

	case "coord_claim":
		var args struct {
			Task       string `json:"task"`
			TTLSeconds int    `json:"ttlSeconds"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.TTLSeconds == 0 {
			args.TTLSeconds = 900
		}
		claim, err := s.coord.ClaimTask(ctx, agentID, args.Task, args.TTLSeconds)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Claimed: %q (expires in %ds)", claim.Task, claim.TTLSeconds))

	case "coord_signal":
		var args struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		json.Unmarshal(params.Arguments, &args)
		if err := s.coord.Broadcast(ctx, agentID, args.Type, args.Payload); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Signal broadcast: %s — %s", args.Type, args.Payload))

	case "route_recommend":
		var args struct {
			TaskDescription string `json:"taskDescription"`
			Budget          string `json:"budget"`
		}
		json.Unmarshal(params.Arguments, &args)
		dec := s.router.Recommend(args.TaskDescription, args.Budget)
		data, _ := json.Marshal(dec)
		return textResult(req.ID, string(data))

	case "health_report":
		report := s.router.HealthReport()
		data, _ := json.Marshal(report)
		return textResult(req.ID, string(data))

	case "dispatch_event":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		var args struct {
			EventType string            `json:"eventType"`
			Source    string            `json:"source"`
			Repo     string            `json:"repo"`
			Payload  map[string]string `json:"payload"`
			Priority int               `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.EventType == "" {
			return errorResp(req.ID, -32602, "eventType is required")
		}
		event := dispatch.Event{
			Type:     dispatch.EventType(args.EventType),
			Source:   args.Source,
			Repo:     args.Repo,
			Payload:  args.Payload,
			Priority: args.Priority,
		}
		results, err := s.dispatcher.DispatchEvent(ctx, event)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(results)
		return textResult(req.ID, string(data))

	case "dispatch_status":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		depth, _ := s.dispatcher.PendingCount(ctx)
		agents, _ := s.dispatcher.PendingAgents(ctx)
		recent, _ := s.dispatcher.RecentDispatches(ctx, 10)

		status := map[string]interface{}{
			"queue_depth":       depth,
			"pending_agents":    agents,
			"recent_dispatches": recent,
		}
		data, _ := json.Marshal(status)
		return textResult(req.ID, string(data))

	case "dispatch_trigger":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		var args struct {
			Agent    string `json:"agent"`
			Priority int    `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent == "" {
			return errorResp(req.ID, -32602, "agent name is required")
		}
		event := dispatch.Event{
			Type:     dispatch.EventManual,
			Source:   "mcp",
			Payload:  map[string]string{"triggered_by": agentID},
		}
		result, err := s.dispatcher.Dispatch(ctx, event, args.Agent, args.Priority)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(result)
		return textResult(req.ID, string(data))

	default:
		return errorResp(req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

func textResult(id interface{}, text string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	}
}

func errorResp(id interface{}, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

func toolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        "memory_store",
			Description: "Store a learning in the swarm knowledge base, tagged with your identity and topics.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]string{"type": "string", "description": "What you learned / observed / decided"},
					"topics":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Topic tags"},
				},
				"required": []string{"content", "topics"},
			},
		},
		{
			Name:        "memory_recall",
			Description: "Search the swarm knowledge base. Returns relevant learnings from all agents.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "What are you looking for?"},
					"limit": map[string]interface{}{"type": "number", "description": "Max results (default 5)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "memory_status",
			Description: "See what other agents in the swarm are currently working on.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "coord_claim",
			Description: "Claim a task so no other agent duplicates your work. Auto-expires.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task":       map[string]string{"type": "string", "description": "What you are working on"},
					"ttlSeconds": map[string]interface{}{"type": "number", "description": "Claim duration in seconds (default 900)"},
				},
				"required": []string{"task"},
			},
		},
		{
			Name:        "coord_signal",
			Description: "Broadcast a signal to the swarm — completion, blocker, or need-help.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":    map[string]interface{}{"type": "string", "enum": []string{"completed", "blocked", "need-help", "directive"}, "description": "Signal type"},
					"payload": map[string]string{"type": "string", "description": "Details"},
				},
				"required": []string{"type", "payload"},
			},
		},
		{
			Name:        "route_recommend",
			Description: "Get the recommended driver for a task based on cost tier and driver health. Returns cheapest healthy driver with fallback options.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"taskDescription": map[string]string{"type": "string", "description": "Describe the task"},
					"budget":          map[string]interface{}{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "Budget tier — low (local only), medium (local+subscription+cli), high (all)"},
				},
				"required": []string{"taskDescription"},
			},
		},
		{
			Name:        "health_report",
			Description: "Get current health status of all drivers in the swarm — circuit breaker state, failure counts, last success/failure timestamps.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "dispatch_event",
			Description: "Submit an event for routing through the dispatcher. The event is matched against rules and dispatched to the appropriate agent(s) with coordination, cooldown, and budget checks.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"eventType": map[string]interface{}{"type": "string", "enum": []string{"pr.opened", "pr.updated", "ci.completed", "timer", "budget.change", "manual", "slack.action"}, "description": "Event type"},
					"source":    map[string]string{"type": "string", "description": "Event source (github, cron, slack, manual)"},
					"repo":      map[string]string{"type": "string", "description": "Repository full name (e.g. AgentGuardHQ/agentguard)"},
					"payload":   map[string]interface{}{"type": "object", "description": "Event-specific key-value data"},
					"priority":  map[string]interface{}{"type": "number", "description": "Priority (0=critical, 1=high, 2=normal, 3=background)"},
				},
				"required": []string{"eventType"},
			},
		},
		{
			Name:        "dispatch_status",
			Description: "Show current dispatch queue depth, pending agents, and recent dispatch decisions.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "dispatch_trigger",
			Description: "Manually trigger an agent run. Bypasses event matching but still respects cooldown and coordination claims.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent":    map[string]string{"type": "string", "description": "Agent name to trigger"},
					"priority": map[string]interface{}{"type": "number", "description": "Priority (0=critical, 1=high, 2=normal, 3=background). Default: 1"},
				},
				"required": []string{"agent"},
			},
		},
	}
}
