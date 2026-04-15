package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/routing"
)

// SwarmTodayReport is the 1-screen daily observability view.
//
// Spec: octi#224. Replaces the daily-checkin role of dispatch_status,
// health_report, and agent_leaderboard by answering a single question
// in one tool call: "is the swarm healthy today?"
type SwarmTodayReport struct {
	Date        string                `json:"date"`
	WindowHours int                   `json:"window_hours"`
	PRs         PRSlice               `json:"prs"`
	Issues      IssueSlice            `json:"issues"`
	Tiers       map[string]TierCounts `json:"tiers"`
	Swarm       SwarmSlice            `json:"swarm"`
	Drivers     DriversSlice          `json:"drivers"`
	Budget      BudgetSlice           `json:"budget"`
	Alerts      AlertsSlice           `json:"alerts"`
	Text        string                `json:"text"`
	Notes       []string              `json:"notes,omitempty"`
}

type PRSlice struct {
	Opened       int `json:"opened"`
	Merged       int `json:"merged"`
	InReview     int `json:"in_review"`
	DeltaVs7dAvg int `json:"delta_vs_7d_avg"`
}

type IssueSlice struct {
	Filed     int   `json:"filed"`
	Triaged   int   `json:"triaged"`
	Stuck     int   `json:"stuck"`
	StuckRefs []int `json:"stuck_refs,omitempty"`
}

type TierCounts struct {
	Dispatches  int  `json:"dispatches"`
	Completions int  `json:"completions"`
	SilentLoss  bool `json:"silent_loss,omitempty"`
}

type SwarmSlice struct {
	LastRunAt       string `json:"last_run_at,omitempty"`
	LastRunWorkflow string `json:"last_run_workflow,omitempty"`
	RunsToday       int    `json:"runs_today"`
	FailuresToday   int    `json:"failures_today"`
	DryStreakHours  int    `json:"dry_streak_hours"`
}

type DriversSlice struct {
	CircuitClosed int      `json:"circuit_closed"`
	StaleGt48h    int      `json:"stale_gt_48h"`
	StaleNames    []string `json:"stale_names,omitempty"`
}

type BudgetSlice struct {
	TodayUSD    float64 `json:"today_usd"`
	MonthUSD    float64 `json:"month_usd"`
	MonthCapUSD float64 `json:"month_cap_usd"`
}

type AlertsSlice struct {
	SilentDispatches int `json:"silent_dispatches"`
	StuckAgents      int `json:"stuck_agents"`
	StaleDrivers     int `json:"stale_drivers"`
}

// swarmTodayInputs is the injectable data layer for testability.
type swarmTodayInputs struct {
	now          time.Time
	recent       []dispatch.DispatchRecord
	drivers      []routing.DriverHealth
	budgetTodayC int
	budgetMonthC int
	budgetCapC   int
	prSearch     func(ctx context.Context, since time.Time) (opened, merged, inReview int, err error)
	issueSearch  func(ctx context.Context, since time.Time) (filed, triaged, stuck int, stuckRefs []int, err error)
	runSearch    func(ctx context.Context, since time.Time) (lastAt time.Time, workflow string, runs, failures int, err error)
	pr7dAvg      int
}

// SwarmToday builds the report live. Used by the server with real adapters.
func (s *Server) SwarmToday(ctx context.Context, windowHours int) *SwarmTodayReport {
	if windowHours <= 0 {
		windowHours = 24
	}
	in := swarmTodayInputs{now: time.Now().UTC()}

	if s.dispatcher != nil {
		in.recent, _ = s.dispatcher.RecentDispatches(ctx, 500)
	}
	if s.router != nil {
		in.drivers = s.router.HealthReport()
	}
	if s.budgetStore != nil {
		if all, err := s.budgetStore.ListAll(ctx); err == nil {
			for _, b := range all {
				in.budgetMonthC += b.SpentMonthlyCents
				in.budgetCapC += b.BudgetMonthlyCents
			}
		}
	}

	in.prSearch = realPRSearch
	in.issueSearch = realIssueSearch
	in.runSearch = realRunSearch
	return buildSwarmToday(ctx, windowHours, in)
}

// buildSwarmToday is the pure composition — fully testable.
func buildSwarmToday(ctx context.Context, windowHours int, in swarmTodayInputs) *SwarmTodayReport {
	if in.now.IsZero() {
		in.now = time.Now().UTC()
	}
	since := in.now.Add(-time.Duration(windowHours) * time.Hour)

	rep := &SwarmTodayReport{
		Date:        in.now.Format("2006-01-02"),
		WindowHours: windowHours,
		Tiers:       map[string]TierCounts{},
	}

	if in.prSearch != nil {
		if o, m, r, err := in.prSearch(ctx, since); err == nil {
			rep.PRs = PRSlice{Opened: o, Merged: m, InReview: r}
			total := o + m
			if in.pr7dAvg > 0 {
				rep.PRs.DeltaVs7dAvg = total - in.pr7dAvg
			}
		} else {
			rep.Notes = append(rep.Notes, "prs: "+err.Error())
		}
	} else {
		rep.Notes = append(rep.Notes, "prs: gh unavailable")
	}

	if in.issueSearch != nil {
		if f, t, s, refs, err := in.issueSearch(ctx, since); err == nil {
			rep.Issues = IssueSlice{Filed: f, Triaged: t, Stuck: s, StuckRefs: refs}
		} else {
			rep.Notes = append(rep.Notes, "issues: "+err.Error())
		}
	}

	// Tiers — classifyTiers is a temporary bridge until hamilton's tier_activity
	// sink (#226) lands. Once that ships, we read pre-counted tier:{t}:{date}
	// keys directly.
	rep.Tiers = classifyTiers(in.recent, since)

	if in.runSearch != nil {
		if lastAt, wf, runs, fails, err := in.runSearch(ctx, since); err == nil {
			rep.Swarm = SwarmSlice{LastRunWorkflow: wf, RunsToday: runs, FailuresToday: fails}
			if !lastAt.IsZero() {
				rep.Swarm.LastRunAt = lastAt.UTC().Format(time.RFC3339)
				dry := int(in.now.Sub(lastAt).Hours())
				if dry < 0 {
					dry = 0
				}
				rep.Swarm.DryStreakHours = dry
			} else {
				rep.Swarm.DryStreakHours = windowHours
				rep.Notes = append(rep.Notes, "no swarm-worker data")
			}
		} else {
			rep.Notes = append(rep.Notes, "swarm: "+err.Error())
		}
	} else {
		rep.Notes = append(rep.Notes, "no swarm-worker data")
	}

	staleCutoff := in.now.Add(-48 * time.Hour)
	for _, d := range in.drivers {
		if d.CircuitState == "CLOSED" {
			rep.Drivers.CircuitClosed++
		}
		if d.LastSuccess == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, d.LastSuccess); err == nil {
			if ts.Before(staleCutoff) {
				rep.Drivers.StaleGt48h++
				rep.Drivers.StaleNames = append(rep.Drivers.StaleNames, d.Name)
			}
		}
	}
	sort.Strings(rep.Drivers.StaleNames)

	rep.Budget = BudgetSlice{
		TodayUSD:    float64(in.budgetTodayC) / 100.0,
		MonthUSD:    float64(in.budgetMonthC) / 100.0,
		MonthCapUSD: float64(in.budgetCapC) / 100.0,
	}

	var silent int
	for _, tc := range rep.Tiers {
		if tc.SilentLoss {
			silent += tc.Dispatches
		}
	}
	rep.Alerts = AlertsSlice{
		SilentDispatches: silent,
		StuckAgents:      rep.Issues.Stuck,
		StaleDrivers:     rep.Drivers.StaleGt48h,
	}

	rep.Text = renderSwarmTodayText(rep)
	return rep
}

func classifyTiers(recent []dispatch.DispatchRecord, since time.Time) map[string]TierCounts {
	out := map[string]TierCounts{
		"local":   {},
		"actions": {},
		"cloud":   {},
		"desktop": {},
		"human":   {},
	}
	for _, r := range recent {
		ts, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil || ts.Before(since) {
			continue
		}
		tier := tierFor(r.Driver, r.Agent)
		tc := out[tier]
		tc.Dispatches++
		if r.Result == "dispatched" || r.Result == "completed" {
			tc.Completions++
		}
		out[tier] = tc
	}
	for k, tc := range out {
		if tc.Dispatches >= 5 && tc.Completions == 0 {
			tc.SilentLoss = true
			out[k] = tc
		}
	}
	return out
}

func tierFor(driver, agent string) string {
	d := strings.ToLower(driver)
	switch {
	case strings.Contains(d, "gh-actions"), strings.Contains(d, "actions"):
		return "actions"
	case strings.Contains(d, "anthropic"), strings.Contains(d, "claude-api"), strings.Contains(d, "cloud"):
		return "cloud"
	case strings.Contains(d, "clawta"), strings.Contains(d, "ollama"), strings.Contains(d, "local"):
		return "local"
	case strings.Contains(d, "copilot"), strings.Contains(d, "desktop"), strings.Contains(d, "prompt-cli"), strings.Contains(d, "openclaw"):
		return "desktop"
	case strings.Contains(d, "human"), d == "":
		return "human"
	}
	return "human"
}

func renderSwarmTodayText(r *SwarmTodayReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== swarm today (%s UTC) ===\n", r.Date)

	prLine := fmt.Sprintf("PRs:    opened=%d merged=%d review=%d", r.PRs.Opened, r.PRs.Merged, r.PRs.InReview)
	if abs(r.PRs.DeltaVs7dAvg) >= 1 {
		prLine += fmt.Sprintf("   (%+d vs 7d avg)", r.PRs.DeltaVs7dAvg)
	}
	b.WriteString(prLine + "\n")

	issLine := fmt.Sprintf("Issues: filed=%d triaged=%d stuck=%d", r.Issues.Filed, r.Issues.Triaged, r.Issues.Stuck)
	if len(r.Issues.StuckRefs) > 0 {
		refs := make([]string, 0, len(r.Issues.StuckRefs))
		for _, n := range r.Issues.StuckRefs {
			refs = append(refs, fmt.Sprintf("#%d", n))
		}
		issLine += "   (stuck! see " + strings.Join(refs, ", ") + ")"
	}
	b.WriteString(issLine + "\n")

	tierOrder := []string{"local", "actions", "cloud", "desktop", "human"}
	parts := []string{}
	var silentNote string
	for _, t := range tierOrder {
		tc := r.Tiers[t]
		cell := fmt.Sprintf("%s=%d", t, tc.Dispatches)
		if tc.SilentLoss {
			cell = fmt.Sprintf("%s=%d/%d*", t, tc.Dispatches, tc.Completions)
			silentNote = "silent-loss suspected"
		}
		parts = append(parts, cell)
	}
	b.WriteString("Tiers:  " + strings.Join(parts, " ") + "\n")
	if silentNote != "" {
		b.WriteString("        *" + silentNote + "\n")
	}

	if r.Swarm.LastRunAt != "" {
		wf := r.Swarm.LastRunWorkflow
		if wf == "" {
			wf = "unknown"
		}
		fmt.Fprintf(&b, "Swarm:  last-run=%s (%s)\n", r.Swarm.LastRunAt, wf)
		fmt.Fprintf(&b, "        runs-today=%d failures=%d dry-streak=%dh\n",
			r.Swarm.RunsToday, r.Swarm.FailuresToday, r.Swarm.DryStreakHours)
	} else {
		b.WriteString("Swarm:  no swarm-worker data\n")
	}

	drv := fmt.Sprintf("Drivers: %d circuit=CLOSED", r.Drivers.CircuitClosed)
	if r.Drivers.StaleGt48h > 0 {
		drv += fmt.Sprintf("  %d stale>48h (%s)", r.Drivers.StaleGt48h,
			strings.Join(r.Drivers.StaleNames, ", "))
	}
	b.WriteString(drv + "\n")

	fmt.Fprintf(&b, "Budget: today=$%.2f  month=$%.2f / $%.2f\n",
		r.Budget.TodayUSD, r.Budget.MonthUSD, r.Budget.MonthCapUSD)
	fmt.Fprintf(&b, "Alerts: silent_dispatches=%d stuck_agents=%d stale_drivers=%d\n",
		r.Alerts.SilentDispatches, r.Alerts.StuckAgents, r.Alerts.StaleDrivers)
	return b.String()
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// --- real `gh` adapters ---

func realPRSearch(ctx context.Context, since time.Time) (int, int, int, error) {
	q := fmt.Sprintf("is:pr org:chitinhq updated:>=%s", since.Format("2006-01-02"))
	items, err := ghSearchIssues(ctx, q)
	if err != nil {
		return 0, 0, 0, err
	}
	var opened, merged, inReview int
	for _, it := range items {
		createdAt, _ := time.Parse(time.RFC3339, it.CreatedAt)
		if !createdAt.IsZero() && !createdAt.Before(since) {
			opened++
		}
		if it.State == "closed" && it.PullRequest != nil && it.PullRequest.MergedAt != "" {
			mergedAt, _ := time.Parse(time.RFC3339, it.PullRequest.MergedAt)
			if !mergedAt.Before(since) {
				merged++
			}
		}
		if it.State == "open" && (it.Comments > 0 || len(it.RequestedReviewers) > 0) {
			inReview++
		}
	}
	return opened, merged, inReview, nil
}

func realIssueSearch(ctx context.Context, since time.Time) (int, int, int, []int, error) {
	q := fmt.Sprintf("is:issue org:chitinhq created:>=%s", since.Format("2006-01-02"))
	items, err := ghSearchIssues(ctx, q)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	stuckCutoff := time.Now().UTC().Add(-72 * time.Hour)
	var filed, triaged, stuck int
	var refs []int
	for _, it := range items {
		filed++
		if len(it.Labels) > 0 {
			triaged++
		}
		if isStuck(it, stuckCutoff) {
			stuck++
			refs = append(refs, it.Number)
		}
	}
	return filed, triaged, stuck, refs, nil
}

func isStuck(it ghIssue, cutoff time.Time) bool {
	for _, l := range it.Labels {
		name := strings.ToLower(l.Name)
		if name == "stuck" || name == "agent:stuck" || name == "agent:blocked" {
			return true
		}
	}
	if it.State != "open" {
		return false
	}
	if it.Comments > 0 {
		return false
	}
	created, err := time.Parse(time.RFC3339, it.CreatedAt)
	if err != nil {
		return false
	}
	return created.Before(cutoff)
}

func realRunSearch(ctx context.Context, since time.Time) (time.Time, string, int, int, error) {
	path := fmt.Sprintf("/repos/chitinhq/chitin/actions/runs?created=>=%s&per_page=50",
		since.Format("2006-01-02"))
	out, err := exec.CommandContext(ctx, "gh", "api", path).Output()
	if err != nil {
		return time.Time{}, "", 0, 0, err
	}
	var resp struct {
		WorkflowRuns []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
			UpdatedAt  string `json:"updated_at"`
		} `json:"workflow_runs"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return time.Time{}, "", 0, 0, err
	}
	var lastAt time.Time
	var wf string
	var runs, fails int
	for _, r := range resp.WorkflowRuns {
		if !strings.Contains(strings.ToLower(r.Name), "swarm") {
			continue
		}
		runs++
		if r.Conclusion == "failure" {
			fails++
		}
		ts, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		if ts.After(lastAt) {
			lastAt = ts
			wf = r.Name
		}
	}
	return lastAt, wf, runs, fails, nil
}

type ghIssue struct {
	Number             int       `json:"number"`
	State              string    `json:"state"`
	CreatedAt          string    `json:"created_at"`
	Comments           int       `json:"comments"`
	Labels             []ghLabel `json:"labels"`
	RequestedReviewers []any     `json:"requested_reviewers"`
	PullRequest        *struct {
		MergedAt string `json:"merged_at"`
	} `json:"pull_request,omitempty"`
}

type ghLabel struct {
	Name string `json:"name"`
}

func ghSearchIssues(ctx context.Context, q string) ([]ghIssue, error) {
	out, err := exec.CommandContext(ctx, "gh", "api", "-X", "GET", "/search/issues",
		"-f", "q="+q, "-F", "per_page=100").Output()
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []ghIssue `json:"items"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}
