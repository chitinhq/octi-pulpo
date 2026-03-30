package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/budget"
	"github.com/AgentGuardHQ/octi-pulpo/internal/coordination"
	"github.com/AgentGuardHQ/octi-pulpo/internal/dispatch"
	"github.com/AgentGuardHQ/octi-pulpo/internal/memory"
	"github.com/AgentGuardHQ/octi-pulpo/internal/org"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
	"github.com/AgentGuardHQ/octi-pulpo/internal/standup"
	"github.com/redis/go-redis/v9"
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
	mem          *memory.Store
	coord        *coordination.Engine
	router       *routing.Router
	dispatcher   *dispatch.Dispatcher
	sprintStore  *sprint.Store
	standupStore *standup.Store
	benchmark    *dispatch.BenchmarkTracker
	profiles     *dispatch.ProfileStore
	orgStore     *org.OrgStore
	budgetStore  *budget.BudgetStore
	goalStore    *sprint.GoalStore
	rdb          *redis.Client
	redisNS      string
}

// New creates an MCP server backed by the given memory and coordination engines.
func New(mem *memory.Store, coord *coordination.Engine, router *routing.Router) *Server {
	return &Server{mem: mem, coord: coord, router: router}
}

// SetDispatcher adds dispatch capabilities to the MCP server.
func (s *Server) SetDispatcher(d *dispatch.Dispatcher) {
	s.dispatcher = d
}

// SetSprintStore enables sprint-related MCP tools.
func (s *Server) SetSprintStore(ss *sprint.Store) {
	s.sprintStore = ss
}

// SetBenchmark enables throughput metrics MCP tools.
func (s *Server) SetBenchmark(bt *dispatch.BenchmarkTracker) {
	s.benchmark = bt
}

// SetStandupStore enables the standup MCP tools.
func (s *Server) SetStandupStore(ss *standup.Store) {
	s.standupStore = ss
}

// SetProfileStore enables the agent leaderboard MCP tool.
func (s *Server) SetProfileStore(ps *dispatch.ProfileStore) {
	s.profiles = ps
}

// SetRedis enables Redis-backed budget enrichment for the health_report tool.
// Budget percentages are written by octi-worker after each agent run.
func (s *Server) SetRedis(rdb *redis.Client, ns string) {
	s.rdb = rdb
	s.redisNS = ns
}

func (s *Server) SetOrgStore(o *org.OrgStore) {
	s.orgStore = o
}

func (s *Server) SetBudgetStore(b *budget.BudgetStore) {
	s.budgetStore = b
}

func (s *Server) SetGoalStore(g *sprint.GoalStore) {
	s.goalStore = g
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
			Content        string   `json:"content"`
			Topics         []string `json:"topics"`
			SquadNamespace string   `json:"squadNamespace"`
		}
		json.Unmarshal(params.Arguments, &args)
		store := s.mem
		if args.SquadNamespace != "" {
			if err := s.mem.RegisterSquad(ctx, args.SquadNamespace); err != nil {
				fmt.Fprintf(os.Stderr, "register squad: %v\n", err)
			}
			store = s.mem.WithSquad(args.SquadNamespace)
		}
		id, err := store.Put(ctx, agentID, args.Content, args.Topics)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("Stored memory %s (topics: %s)", id, strings.Join(args.Topics, ", "))
		if args.SquadNamespace != "" {
			msg += fmt.Sprintf(" [squad: %s]", args.SquadNamespace)
		}
		return textResult(req.ID, msg)

	case "memory_recall":
		var args struct {
			Query          string `json:"query"`
			Limit          int    `json:"limit"`
			SquadNamespace string `json:"squadNamespace"`
			CrossSquad     bool   `json:"crossSquad"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Limit == 0 {
			args.Limit = 5
		}
		var results []memory.Entry
		var err error
		switch {
		case args.CrossSquad:
			results, err = s.mem.RecallCrossSquad(ctx, args.Query, args.Limit)
		case args.SquadNamespace != "":
			results, err = s.mem.WithSquad(args.SquadNamespace).Recall(ctx, args.Query, args.Limit)
		default:
			results, err = s.mem.Recall(ctx, args.Query, args.Limit)
		}
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
		report := s.enrichHealthReport(ctx, s.router.HealthReport())
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
			Budget   string `json:"budget"` // optional: "low", "medium", "high"; empty = dynamic
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent == "" {
			return errorResp(req.ID, -32602, "agent name is required")
		}
		event := dispatch.Event{
			Type:    dispatch.EventManual,
			Source:  "mcp",
			Payload: map[string]string{"triggered_by": agentID},
		}
		var result dispatch.DispatchResult
		var err error
		if args.Budget != "" {
			result, err = s.dispatcher.DispatchBudget(ctx, event, args.Agent, args.Priority, args.Budget)
		} else {
			result, err = s.dispatcher.Dispatch(ctx, event, args.Agent, args.Priority)
		}
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(result)
		return textResult(req.ID, string(data))

	case "sprint_status":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		items, err := s.sprintStore.GetAll(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		// Group by squad
		grouped := make(map[string][]sprint.SprintItem)
		for _, item := range items {
			grouped[item.Squad] = append(grouped[item.Squad], item)
		}
		data, _ := json.Marshal(grouped)
		return textResult(req.ID, string(data))

	case "sprint_sync":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var synced []string
		for _, repo := range sprint.DefaultRepos {
			if err := s.sprintStore.Sync(ctx, repo); err != nil {
				synced = append(synced, fmt.Sprintf("%s: error: %v", repo, err))
			} else {
				synced = append(synced, fmt.Sprintf("%s: synced", repo))
			}
		}
		return textResult(req.ID, strings.Join(synced, "\n"))

	case "sprint_create":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo      string `json:"repo"`
			IssueNum  int    `json:"issue_num"`
			Title     string `json:"title"`
			Priority  int    `json:"priority"`
			DependsOn []int  `json:"depends_on"`
			AssignTo  string `json:"assign_to"`
			Squad     string `json:"squad"`
		}
		json.Unmarshal(params.Arguments, &args)
		item := sprint.SprintItem{
			Repo:      args.Repo,
			IssueNum:  args.IssueNum,
			Title:     args.Title,
			Priority:  args.Priority,
			DependsOn: args.DependsOn,
			AssignTo:  args.AssignTo,
			Squad:     args.Squad,
		}
		if err := s.sprintStore.Create(ctx, item); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Sprint item created: %s#%d — %s (priority: %d)", args.Repo, args.IssueNum, args.Title, args.Priority))

	case "sprint_reprioritize":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo     string `json:"repo"`
			IssueNum int    `json:"issue_num"`
			Priority int    `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.IssueNum == 0 {
			return errorResp(req.ID, -32602, "issue_num is required")
		}
		if args.Repo == "" {
			// Search all default repos
			found := false
			for _, repo := range sprint.DefaultRepos {
				if err := s.sprintStore.Reprioritize(ctx, repo, args.IssueNum, args.Priority); err == nil {
					args.Repo = repo
					found = true
					break
				}
			}
			if !found {
				return errorResp(req.ID, -32000, fmt.Sprintf("issue #%d not found in any sprint repo", args.IssueNum))
			}
		} else {
			if err := s.sprintStore.Reprioritize(ctx, args.Repo, args.IssueNum, args.Priority); err != nil {
				return errorResp(req.ID, -32000, err.Error())
			}
		}
		priorityLabel := [3]string{"P0 (critical)", "P1 (high)", "P2 (normal)"}
		label := "custom"
		if args.Priority >= 0 && args.Priority <= 2 {
			label = priorityLabel[args.Priority]
		}
		return textResult(req.ID, fmt.Sprintf("%s#%d reprioritized to %s", args.Repo, args.IssueNum, label))

	case "sprint_complete":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo     string `json:"repo"`
			IssueNum int    `json:"issue_num"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.IssueNum == 0 {
			return errorResp(req.ID, -32602, "issue_num is required")
		}
		if args.Repo == "" {
			for _, repo := range sprint.DefaultRepos {
				unblocked, err := s.sprintStore.Complete(ctx, repo, args.IssueNum)
				if err == nil {
					args.Repo = repo
					msg := fmt.Sprintf("%s#%d marked done", repo, args.IssueNum)
					if len(unblocked) > 0 {
						nums := make([]string, len(unblocked))
						for i, n := range unblocked {
							nums[i] = fmt.Sprintf("#%d", n)
						}
						msg += fmt.Sprintf("; unblocked: %s", strings.Join(nums, ", "))
					}
					return textResult(req.ID, msg)
				}
			}
			return errorResp(req.ID, -32000, fmt.Sprintf("issue #%d not found in any sprint repo", args.IssueNum))
		}
		unblocked, err := s.sprintStore.Complete(ctx, args.Repo, args.IssueNum)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("%s#%d marked done", args.Repo, args.IssueNum)
		if len(unblocked) > 0 {
			nums := make([]string, len(unblocked))
			for i, n := range unblocked {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			msg += fmt.Sprintf("; unblocked: %s", strings.Join(nums, ", "))
		}
		return textResult(req.ID, msg)

	case "benchmark_status":
		if s.benchmark == nil {
			return errorResp(req.ID, -32000, "benchmark tracker not initialized")
		}
		metrics, err := s.benchmark.Compute(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(metrics)
		return textResult(req.ID, string(data))

	case "agent_leaderboard":
		if s.profiles == nil {
			return errorResp(req.ID, -32000, "profile store not initialized")
		}
		entries, err := s.profiles.Leaderboard(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, dispatch.FormatLeaderboard(entries))

	case "request_work":
		var args struct {
			ToSquad         string `json:"to_squad"`
			Type            string `json:"type"`
			Description     string `json:"description"`
			Priority        int    `json:"priority"`
			DeadlineMinutes int    `json:"deadline_minutes"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.ToSquad == "" || args.Description == "" {
			return errorResp(req.ID, -32602, "to_squad and description are required")
		}
		crossReq, err := s.coord.SubmitRequest(ctx, agentID, args.ToSquad,
			coordination.RequestType(args.Type), args.Description,
			args.Priority, args.DeadlineMinutes)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf(
			"Request %s submitted to squad %s (type: %s, priority: %d)",
			crossReq.ID, crossReq.ToSquad, crossReq.Type, crossReq.Priority,
		))

	case "check_requests":
		var args struct {
			Squad string `json:"squad"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Squad == "" {
			return errorResp(req.ID, -32602, "squad is required")
		}
		pending, err := s.coord.GetPendingRequests(ctx, args.Squad)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(pending) == 0 {
			return textResult(req.ID, fmt.Sprintf("No pending requests for squad %s.", args.Squad))
		}
		data, _ := json.Marshal(pending)
		return textResult(req.ID, string(data))

	case "fulfill_request":
		var args struct {
			RequestID string `json:"request_id"`
			Result    string `json:"result"`
			PRNumber  int    `json:"pr_number"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.RequestID == "" || args.Result == "" {
			return errorResp(req.ID, -32602, "request_id and result are required")
		}
		if err := s.coord.FulfillRequest(ctx, args.RequestID, agentID, args.Result, args.PRNumber); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("Request %s fulfilled.", args.RequestID)
		if args.PRNumber > 0 {
			msg += fmt.Sprintf(" PR #%d linked.", args.PRNumber)
		}
		return textResult(req.ID, msg)

	case "standup_report":
		if s.standupStore == nil {
			return errorResp(req.ID, -32000, "standup store not initialized")
		}
		var args standup.Entry
		json.Unmarshal(params.Arguments, &args)
		if args.Squad == "" {
			args.Squad = agentID
		}
		if err := s.standupStore.Report(ctx, args); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Standup recorded for squad %q on %s", args.Squad, args.Date))

	case "standup_read":
		if s.standupStore == nil {
			return errorResp(req.ID, -32000, "standup store not initialized")
		}
		var args struct {
			Squad string `json:"squad"`
			All   bool   `json:"all"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.All {
			entries, err := s.standupStore.Daily(ctx)
			if err != nil {
				return errorResp(req.ID, -32000, err.Error())
			}
			if len(entries) == 0 {
				return textResult(req.ID, "No standup entries found for today.")
			}
			data, _ := json.Marshal(entries)
			return textResult(req.ID, string(data))
		}
		if args.Squad == "" {
			return errorResp(req.ID, -32602, "squad is required (or set all=true for all squads)")
		}
		entry, err := s.standupStore.Read(ctx, args.Squad)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if entry == nil {
			return textResult(req.ID, fmt.Sprintf("No standup found for squad %q today.", args.Squad))
		}
		data, _ := json.Marshal(entry)
		return textResult(req.ID, string(data))


	case "org_chart":
		if s.orgStore == nil {
			return errorResp(req.ID, -32000, "org store not initialized")
		}
		var args struct {
			Agent string `json:"agent"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent != "" {
			agent, err := s.orgStore.Get(ctx, args.Agent)
			if err != nil {
				return errorResp(req.ID, -32000, err.Error())
			}
			chain, _ := s.orgStore.ChainOfCommand(ctx, args.Agent)
			reports, _ := s.orgStore.DirectReports(ctx, args.Agent)
			result := map[string]interface{}{
				"agent":          agent,
				"chain":          chain,
				"direct_reports": reports,
			}
			data, _ := json.Marshal(result)
			return textResult(req.ID, string(data))
		}
		tree, err := org.PrintTree(ctx, s.orgStore)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, tree)

	case "circuit_reset":
		var args struct {
			Driver string `json:"driver"`
			Note   string `json:"note"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Driver == "" {
			return errorResp(req.ID, -32602, "driver is required")
		}
		// Capture previous state for the response message.
		prev := routing.DriverHealth{Name: args.Driver}
		for _, h := range s.router.HealthReport() {
			if h.Name == args.Driver {
				prev = h
				break
			}
		}
		newState, err := s.router.ForceClose(args.Driver)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("circuit_reset: %s %s→CLOSED (failures %d→0)",
			args.Driver, prev.CircuitState, prev.Failures)
		if args.Note != "" {
			msg += " — " + args.Note
		}
		data, _ := json.Marshal(newState)
		return textResult(req.ID, msg+"\n"+string(data))

	default:
		return errorResp(req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

// EnrichedHealthEntry extends DriverHealth with derived observability fields.
type EnrichedHealthEntry struct {
	Name               string `json:"name"`
	CircuitState       string `json:"circuit_state"`
	Failures           int    `json:"failures"`
	LastFailure        string `json:"last_failure,omitempty"`
	LastSuccess        string `json:"last_success,omitempty"`
	SecsSinceSuccess   int64  `json:"secs_since_last_success,omitempty"`
	Recommendation     string `json:"recommendation"`
}

// enrichHealthReport adds derived fields to each DriverHealth entry.
func enrichHealthReport(drivers []routing.DriverHealth) []EnrichedHealthEntry {
	now := time.Now().UTC()
	entries := make([]EnrichedHealthEntry, 0, len(drivers))
	for _, d := range drivers {
		e := EnrichedHealthEntry{
			Name:         d.Name,
			CircuitState: d.CircuitState,
			Failures:     d.Failures,
			LastFailure:  d.LastFailure,
			LastSuccess:  d.LastSuccess,
		}

		if d.LastSuccess != "" {
			if t, err := time.Parse(time.RFC3339, d.LastSuccess); err == nil {
				e.SecsSinceSuccess = int64(now.Sub(t).Seconds())
			}
		}

		switch d.CircuitState {
		case "OPEN":
			e.Recommendation = fmt.Sprintf("%s: budget exhausted or unreachable — check quota and reset circuit", d.Name)
		case "HALF":
			e.Recommendation = fmt.Sprintf("%s: recovering — use with caution, monitor next run", d.Name)
		default:
			if e.SecsSinceSuccess > 3600 {
				e.Recommendation = fmt.Sprintf("%s: healthy but no success in %dh — verify driver is reachable", d.Name, e.SecsSinceSuccess/3600)
			} else {
				e.Recommendation = fmt.Sprintf("%s: healthy", d.Name)
			}
		}

		entries = append(entries, e)
	}
	return entries
}

// enrichHealthReport adds Redis-backed budget data and recommended actions to a
// raw HealthReport. Drivers without Redis budget data get nil BudgetPct so the
// client can distinguish "unknown" from "0%".
func (s *Server) enrichHealthReport(ctx context.Context, drivers []routing.DriverHealth) []routing.DriverHealth {
	for i, h := range drivers {
		if s.rdb != nil {
			budgetKey := s.redisNS + ":driver-budget:" + h.Name
			vals, err := s.rdb.HGetAll(ctx, budgetKey).Result()
			if err == nil && len(vals) > 0 {
				if pctStr, ok := vals["pct"]; ok {
					if pct, err := strconv.Atoi(pctStr); err == nil {
						drivers[i].BudgetPct = &pct
					}
				}
			}
		}
		drivers[i].RecommendedAction = routing.RecommendAction(drivers[i])
	}
	return drivers
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
			Description: "Store a learning in the swarm knowledge base, tagged with your identity and topics. Pass squadNamespace to isolate memories by squad.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content":        map[string]string{"type": "string", "description": "What you learned / observed / decided"},
					"topics":         map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Topic tags"},
					"squadNamespace": map[string]string{"type": "string", "description": "Optional squad namespace for isolation (e.g. 'octi-pulpo', 'agentguard'). Omit for root namespace."},
				},
				"required": []string{"content", "topics"},
			},
		},
		{
			Name:        "memory_recall",
			Description: "Search the swarm knowledge base. Scoped by squadNamespace when provided, or cross-squad when crossSquad=true.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query":          map[string]string{"type": "string", "description": "What are you looking for?"},
					"limit":          map[string]interface{}{"type": "number", "description": "Max results (default 5)"},
					"squadNamespace": map[string]string{"type": "string", "description": "Search within a specific squad namespace. Omit for root namespace."},
					"crossSquad":     map[string]interface{}{"type": "boolean", "description": "Search across all squad namespaces (overrides squadNamespace). Default false."},
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
			Description: "Get current health status of all drivers in the swarm — circuit breaker state, failure counts, last success/failure timestamps, time since last success, and recommended actions per driver.",
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
		{
			Name:        "sprint_status",
			Description: "Return all sprint items grouped by squad. Shows issue numbers, titles, priority, status, and dependencies.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "sprint_sync",
			Description: "Trigger a sync of sprint items from GitHub issues across all tracked repos.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "sprint_create",
			Description: "Manually create or upsert a sprint item. Use when an agent identifies work during brainstorm/research that should flow into the sprint backlog, or to pre-load items with explicit priority and dependency chains before sprint_sync runs.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":       map[string]string{"type": "string", "description": "Repo (e.g. AgentGuardHQ/octi-pulpo)"},
					"issue_num":  map[string]interface{}{"type": "number", "description": "GitHub issue number. Use 0 if not backed by a GitHub issue."},
					"title":      map[string]string{"type": "string", "description": "Sprint item title"},
					"priority":   map[string]interface{}{"type": "number", "enum": []int{0, 1, 2}, "description": "Priority: 0=P0 critical, 1=P1 high, 2=P2 normal"},
					"depends_on": map[string]interface{}{"type": "array", "items": map[string]string{"type": "number"}, "description": "Issue numbers that must complete before this item can be dispatched"},
					"assign_to":  map[string]string{"type": "string", "description": "Agent name to assign (e.g. sr-kernel-01). Leave empty for auto-dispatch."},
					"squad":      map[string]string{"type": "string", "description": "Squad name. Inferred from repo if omitted."},
				},
				"required": []string{"repo", "issue_num", "title"},
			},
		},
		{
			Name:        "sprint_reprioritize",
			Description: "Change the priority of a sprint item. Use when the CTO says 'make this P0' or 'deprioritize this'. Affects dispatch order — P0 items are dispatched before P1/P2.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"issue_num": map[string]interface{}{"type": "number", "description": "GitHub issue number to reprioritize"},
					"priority":  map[string]interface{}{"type": "number", "enum": []int{0, 1, 2}, "description": "New priority: 0=P0 critical, 1=P1 high, 2=P2 normal"},
					"repo":      map[string]string{"type": "string", "description": "Repo (e.g. AgentGuardHQ/octi-pulpo). If omitted, all tracked repos are searched."},
				},
				"required": []string{"issue_num", "priority"},
			},
		},
		{
			Name:        "sprint_complete",
			Description: "Mark a sprint item as done. Unblocks any dependent items. Call after merging a PR or closing an issue outside of the normal sync cycle.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"issue_num": map[string]interface{}{"type": "number", "description": "GitHub issue number to mark done"},
					"repo":      map[string]string{"type": "string", "description": "Repo (e.g. AgentGuardHQ/octi-pulpo). If omitted, all tracked repos are searched."},
				},
				"required": []string{"issue_num"},
			},
		},
		{
			Name:        "benchmark_status",
			Description: "Return swarm throughput and health metrics: PRs/hour, commits/run, waste %, budget efficiency, active agents, queue depth, pass rate, and QAI-X composite health index (0-100).",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "org_chart",
			Description: "Return the agent org chart. Without arguments returns the full tree. With an agent name returns that agent's record, chain of command, and direct reports.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"type":        "string",
						"description": "Optional agent name to get specific record + chain of command",
					},
				},
			},
		},
		{
			Name:        "agent_leaderboard",
			Description: "Rank all agents by productivity score. Returns a scored, sorted list with verdicts (promote/retain/monitor/fire) derived from commit output, reliability, and execution duration. Agents with no run history are omitted.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			Name:        "request_work",
			Description: "Request work from another squad. The request is stored and routed to the target squad's agents on their next tick. Use when you need a report, query, review, fix, or deploy from a different squad's domain.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"to_squad":         map[string]string{"type": "string", "description": "Target squad name (e.g. 'analytics', 'kernel', 'shellforge')"},
					"type":             map[string]interface{}{"type": "string", "enum": []string{"report", "query", "review", "fix", "deploy"}, "description": "Work type"},
					"description":      map[string]string{"type": "string", "description": "What you need done"},
					"priority":         map[string]interface{}{"type": "number", "description": "0=urgent, 1=high, 2=normal (default 2)"},
					"deadline_minutes": map[string]interface{}{"type": "number", "description": "How soon you need this (in minutes). Default: 1440 (24h)"},
				},
				"required": []string{"to_squad", "description"},
			},
		},
		{
			Name:        "check_requests",
			Description: "Check for incoming cross-squad work requests targeting your squad. Returns pending requests with age, priority, and description. Call at the start of each run to pick up delegated work.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"squad": map[string]string{"type": "string", "description": "Your squad name (e.g. 'analytics', 'kernel')"},
				},
				"required": []string{"squad"},
			},
		},
		{
			Name:        "fulfill_request",
			Description: "Mark a cross-squad request as complete. Notifies the requesting agent via coord_signal and removes the request from the pending queue.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"request_id": map[string]string{"type": "string", "description": "Request ID returned by request_work or check_requests"},
					"result":     map[string]string{"type": "string", "description": "Summary of the work done / where to find the output"},
					"pr_number":  map[string]interface{}{"type": "number", "description": "PR number if the work resulted in a pull request (optional)"},
				},
				"required": []string{"request_id", "result"},
			},
		},
		{
			Name:        "standup_report",
			Description: "Post your squad's async daily standup. Records what was done, what's in progress, what's blocked, and any cross-squad requests. One entry per squad per day (later calls overwrite).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"squad":    map[string]string{"type": "string", "description": "Squad name (e.g. 'octi-pulpo', 'kernel'). Defaults to your agent ID if omitted."},
					"done":     map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "What was completed since last standup"},
					"doing":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "What is currently in progress"},
					"blocked":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Blockers (issues, PRs waiting for review, etc.)"},
					"requests": map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Cross-squad requests or asks"},
				},
				"required": []string{},
			},
		},
		{
			Name:        "standup_read",
			Description: "Read a squad's standup for today. Pass squad name for a single squad, or set all=true for the full daily summary across all squads.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"squad": map[string]string{"type": "string", "description": "Squad name to read (e.g. 'octi-pulpo'). Required unless all=true."},
					"all":   map[string]interface{}{"type": "boolean", "description": "Set true to return all squads' standups for today."},
				},
				"required": []string{},
			},
		},
		{
			Name:        "circuit_reset",
			Description: "Manually reset a driver circuit breaker from OPEN to CLOSED. Use when you know a driver has recovered (e.g. budget refilled, rate-limit lifted, transient error resolved). Requires the driver to have an existing health file.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"driver": map[string]string{"type": "string", "description": "Driver name to reset (e.g. 'codex', 'copilot', 'gemini'). Must match an existing health file."},
					"note":   map[string]string{"type": "string", "description": "Optional reason for the manual reset (logged in the response for audit purposes)."},
				},
				"required": []string{"driver"},
			},
		},
	}
}
