package sprint

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// issueRefRe matches "Closes #N", "Fixes #N", "Resolves #N" (and plural/past
// tense variants) in PR bodies. Used by SyncPRs to link PRs to sprint items.
var issueRefRe = regexp.MustCompile(`(?i)(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)

// SprintItem represents a single issue in the sprint backlog.
type SprintItem struct {
	Squad     string `json:"squad"`
	IssueNum  int    `json:"issue_num"`
	Repo      string `json:"repo"`
	Title     string `json:"title"`
	Priority  int    `json:"priority"`    // 0=P0, 1=P1, 2=P2
	DependsOn []int  `json:"depends_on"`  // issue numbers that must complete first
	AssignTo  string `json:"assign_to"`   // agent name
	Status    string `json:"status"`      // open, claimed, in_progress, pr_open, done
	PRNumber  int    `json:"pr_number"`
	UpdatedAt string `json:"updated_at"`
}

// Store manages sprint items in Redis, synced from GitHub issues.
type Store struct {
	rdb       *redis.Client
	namespace string
	log       *log.Logger
}

// DefaultRepos is the standard set of repos to sync.
var DefaultRepos = []string{
	"AgentGuardHQ/agentguard",
	"AgentGuardHQ/octi-pulpo",
	"AgentGuardHQ/shellforge",
}

// NewStore creates a sprint store backed by Redis.
func NewStore(rdb *redis.Client, namespace string) *Store {
	return &Store{
		rdb:       rdb,
		namespace: namespace,
		log:       log.New(os.Stderr, "sprint-store: ", log.LstdFlags),
	}
}

// ghIssue is the JSON shape returned by `gh issue list --json`.
type ghIssue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
}

// ghPR is the JSON shape returned by `gh pr list --json number,body`.
type ghPR struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
}

// parseIssueRefs extracts issue numbers referenced in a PR body via standard
// GitHub closing keywords (Closes, Fixes, Resolves — any case, singular or plural).
func parseIssueRefs(body string) []int {
	matches := issueRefRe.FindAllStringSubmatch(body, -1)
	var nums []int
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	return nums
}

// Sync fetches open issues from a GitHub repo and stores them in Redis.
// Issues labeled "sprint" get priority 0, others get priority 2.
// After syncing issues it calls SyncPRs to mark items with open PRs as "pr_open",
// preventing the brain from dispatching agents to re-implement in-flight work.
func (s *Store) Sync(ctx context.Context, repo string) error {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list",
		"-R", repo,
		"--state", "open",
		"--json", "number,title,labels,assignees",
		"-L", "50",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh issue list -R %s: %w", repo, err)
	}

	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return fmt.Errorf("parse gh output for %s: %w", repo, err)
	}

	pipe := s.rdb.Pipeline()
	now := time.Now().UTC().Format(time.RFC3339)

	for _, issue := range issues {
		// Determine priority from labels
		priority := 2
		for _, lbl := range issue.Labels {
			if lbl.Name == "sprint" {
				priority = 0
				break
			}
			if lbl.Name == "P1" || lbl.Name == "p1" {
				priority = 1
			}
		}

		// Determine assignee
		assignTo := ""
		if len(issue.Assignees) > 0 {
			assignTo = issue.Assignees[0].Login
		}

		// Infer squad from repo
		squad := inferSquadFromRepo(repo)

		// Check if item already exists (preserve status)
		key := s.itemKey(repo, issue.Number)
		existing, _ := s.rdb.Get(ctx, key).Result()

		item := SprintItem{
			Squad:     squad,
			IssueNum:  issue.Number,
			Repo:      repo,
			Title:     issue.Title,
			Priority:  priority,
			AssignTo:  assignTo,
			Status:    "open",
			UpdatedAt: now,
		}

		// Preserve status from existing item if it was already tracked
		if existing != "" {
			var prev SprintItem
			if err := json.Unmarshal([]byte(existing), &prev); err == nil {
				if prev.Status != "" && prev.Status != "open" {
					item.Status = prev.Status
				}
				if prev.PRNumber > 0 {
					item.PRNumber = prev.PRNumber
				}
				if len(prev.DependsOn) > 0 {
					item.DependsOn = prev.DependsOn
				}
			}
		}

		data, err := json.Marshal(item)
		if err != nil {
			continue
		}

		pipe.Set(ctx, key, data, 0)
	}

	// Track which repos have been synced
	pipe.SAdd(ctx, s.key("sprint-repos"), repo)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline for %s: %w", repo, err)
	}

	s.log.Printf("synced %d issues from %s", len(issues), repo)

	// Promote items that already have open PRs so the brain doesn't re-dispatch.
	if err := s.SyncPRs(ctx, repo); err != nil {
		s.log.Printf("sync PRs for %s: %v", repo, err)
	}
	// Sync closed issues so items that shipped don't stay status="open" in Redis.
	if err := s.SyncClosed(ctx, repo); err != nil {
		s.log.Printf("sync closed issues for %s: %v", repo, err)
	}
	return nil
}

// SyncPRs fetches open PRs for a repo and transitions sprint items from
// status="open" to status="pr_open" whenever a PR body references the issue via
// a standard GitHub closing keyword (Closes/Fixes/Resolves #N).
// Items already in a non-open state are not modified, so "claimed", "done", etc.
// are preserved. The highest-numbered PR wins when multiple PRs reference the
// same issue.
func (s *Store) SyncPRs(ctx context.Context, repo string) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"-R", repo,
		"--state", "open",
		"--json", "number,body",
		"-L", "100",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh pr list -R %s: %w", repo, err)
	}

	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return fmt.Errorf("parse pr list for %s: %w", repo, err)
	}

	// Build map: issueNum → highest PR number referencing it.
	issueToLatestPR := make(map[int]int)
	for _, pr := range prs {
		for _, issueNum := range parseIssueRefs(pr.Body) {
			if pr.Number > issueToLatestPR[issueNum] {
				issueToLatestPR[issueNum] = pr.Number
			}
		}
	}

	if len(issueToLatestPR) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updated := 0
	for issueNum, prNum := range issueToLatestPR {
		key := s.itemKey(repo, issueNum)
		raw, err := s.rdb.Get(ctx, key).Result()
		if err != nil {
			continue // issue not in sprint store; skip
		}
		var item SprintItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}
		// Only promote open items; preserve claimed/in_progress/done/etc.
		if item.Status != "open" {
			continue
		}
		item.Status = "pr_open"
		item.PRNumber = prNum
		item.UpdatedAt = now

		data, err := json.Marshal(item)
		if err != nil {
			continue
		}
		if err := s.rdb.Set(ctx, key, data, 0).Err(); err != nil {
			s.log.Printf("update pr_open for %s#%d: %v", repo, issueNum, err)
			continue
		}
		updated++
	}

	s.log.Printf("synced %d open PRs from %s (%d items promoted to pr_open)", len(prs), repo, updated)
	return nil
}

// NextMergeable returns sprint items that have an open PR (status="pr_open"),
// sorted by priority (P0 first). The brain uses this to dispatch pr-merger-agent
// rather than re-dispatching SR agents to re-implement already-solved work.
func (s *Store) NextMergeable(ctx context.Context) ([]SprintItem, error) {
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	var mergeable []SprintItem
	for _, item := range all {
		if item.Status == "pr_open" && item.PRNumber > 0 {
			mergeable = append(mergeable, item)
		}
	}

	sort.Slice(mergeable, func(i, j int) bool {
		if mergeable[i].Priority != mergeable[j].Priority {
			return mergeable[i].Priority < mergeable[j].Priority
		}
		return mergeable[i].IssueNum < mergeable[j].IssueNum
	})

	return mergeable, nil
}

// NextDispatchable returns sprint items that are ready to work on:
// status=open, no active claim, all dependencies met (deps have status=done).
// Sorted by priority (P0 first).
func (s *Store) NextDispatchable(ctx context.Context) ([]SprintItem, error) {
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	// Build a set of done issue numbers for dependency checks
	doneSet := make(map[int]bool)
	for _, item := range all {
		if item.Status == "done" {
			doneSet[item.IssueNum] = true
		}
	}

	var dispatchable []SprintItem
	for _, item := range all {
		if item.Status != "open" {
			continue
		}

		// Check all dependencies are met
		depsMet := true
		for _, dep := range item.DependsOn {
			if !doneSet[dep] {
				depsMet = false
				break
			}
		}
		if !depsMet {
			continue
		}

		dispatchable = append(dispatchable, item)
	}

	// Sort by priority (P0 first), then by issue number (FIFO)
	sort.Slice(dispatchable, func(i, j int) bool {
		if dispatchable[i].Priority != dispatchable[j].Priority {
			return dispatchable[i].Priority < dispatchable[j].Priority
		}
		return dispatchable[i].IssueNum < dispatchable[j].IssueNum
	})

	return dispatchable, nil
}

// UpdateStatus updates the status of a sprint item.
func (s *Store) UpdateStatus(ctx context.Context, repo string, issueNum int, status string) error {
	key := s.itemKey(repo, issueNum)
	raw, err := s.rdb.Get(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("get sprint item %s#%d: %w", repo, issueNum, err)
	}

	var item SprintItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return fmt.Errorf("parse sprint item: %w", err)
	}

	item.Status = status
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	return s.rdb.Set(ctx, key, data, 0).Err()
}

// GetAll returns all sprint items across all synced repos.
func (s *Store) GetAll(ctx context.Context) ([]SprintItem, error) {
	repos, err := s.rdb.SMembers(ctx, s.key("sprint-repos")).Result()
	if err != nil {
		return nil, err
	}

	var items []SprintItem
	for _, repo := range repos {
		repoItems, err := s.getByRepo(ctx, repo)
		if err != nil {
			s.log.Printf("get items for %s: %v", repo, err)
			continue
		}
		items = append(items, repoItems...)
	}

	return items, nil
}

// GetBySquad returns sprint items filtered by squad name.
func (s *Store) GetBySquad(ctx context.Context, squad string) ([]SprintItem, error) {
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	var filtered []SprintItem
	for _, item := range all {
		if item.Squad == squad {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

// getByRepo returns all sprint items for a specific repo by scanning Redis keys.
func (s *Store) getByRepo(ctx context.Context, repo string) ([]SprintItem, error) {
	pattern := s.namespace + ":sprint:" + repo + ":*"
	keys, err := s.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return nil, nil
	}

	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	var items []SprintItem
	for _, v := range vals {
		if v == nil {
			continue
		}
		str, ok := v.(string)
		if !ok {
			continue
		}
		var item SprintItem
		if err := json.Unmarshal([]byte(str), &item); err != nil {
			continue
		}
		items = append(items, item)
	}

	return items, nil
}

// SyncClosed fetches recently closed issues from a GitHub repo and marks
// any matching sprint items as "done". This prevents the brain from
// re-dispatching work that has already shipped.
func (s *Store) SyncClosed(ctx context.Context, repo string) error {
	cmd := exec.CommandContext(ctx, "gh", "issue", "list",
		"-R", repo,
		"--state", "closed",
		"--json", "number",
		"-L", "50",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh issue list --state closed -R %s: %w", repo, err)
	}

	var issues []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return fmt.Errorf("parse closed issues for %s: %w", repo, err)
	}

	nums := make([]int, len(issues))
	for i, issue := range issues {
		nums[i] = issue.Number
	}

	marked := s.markClosedItems(ctx, repo, nums)
	if marked > 0 {
		s.log.Printf("marked %d closed issues as done in %s", marked, repo)
	}
	return nil
}

// markClosedItems marks sprint items for the given issue numbers as "done"
// if they are currently open or pr_open. Returns the number of items marked.
func (s *Store) markClosedItems(ctx context.Context, repo string, issueNums []int) int {
	now := time.Now().UTC().Format(time.RFC3339)
	var marked int
	for _, num := range issueNums {
		key := s.itemKey(repo, num)
		raw, err := s.rdb.Get(ctx, key).Result()
		if err != nil {
			continue // not in sprint store
		}
		var item SprintItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}
		if item.Status == "done" {
			continue
		}
		item.Status = "done"
		item.UpdatedAt = now
		data, _ := json.Marshal(item)
		s.rdb.Set(ctx, key, data, 0)
		marked++
	}
	return marked
}

// Reprioritize updates the priority of a sprint item identified by repo + issue number.
// priority: 0=P0 (critical), 1=P1 (high), 2=P2 (normal).
// Returns an error if the item is not found.
func (s *Store) Reprioritize(ctx context.Context, repo string, issueNum int, priority int) error {
	key := s.itemKey(repo, issueNum)
	raw, err := s.rdb.Get(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("sprint item %s#%d not found: %w", repo, issueNum, err)
	}

	var item SprintItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return fmt.Errorf("parse sprint item: %w", err)
	}

	item.Priority = priority
	item.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	return s.rdb.Set(ctx, key, data, 0).Err()
}

// Complete marks a sprint item as "done" and returns the issue numbers of any
// items that are now unblocked (i.e. were waiting on this item as a dependency).
func (s *Store) Complete(ctx context.Context, repo string, issueNum int) (unblocked []int, err error) {
	if err := s.UpdateStatus(ctx, repo, issueNum, "done"); err != nil {
		return nil, err
	}

	// Find items whose DependsOn list includes this issue number and are now fully unblocked.
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	doneSet := make(map[int]bool)
	for _, item := range all {
		if item.Status == "done" {
			doneSet[item.IssueNum] = true
		}
	}

	for _, item := range all {
		if item.Status != "open" {
			continue
		}
		dependsOnCompleted := false
		for _, dep := range item.DependsOn {
			if dep == issueNum {
				dependsOnCompleted = true
				break
			}
		}
		if !dependsOnCompleted {
			continue
		}
		// Check all deps are now met
		allMet := true
		for _, dep := range item.DependsOn {
			if !doneSet[dep] {
				allMet = false
				break
			}
		}
		if allMet {
			unblocked = append(unblocked, item.IssueNum)
		}
	}

	return unblocked, nil
}

func (s *Store) itemKey(repo string, issueNum int) string {
	return s.namespace + ":sprint:" + repo + ":" + strconv.Itoa(issueNum)
}

func (s *Store) key(suffix string) string {
	return s.namespace + ":" + suffix
}

// inferSquadFromRepo maps a repo name to a squad.
func inferSquadFromRepo(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "unknown"
	}
	name := parts[1]
	switch {
	case name == "agentguard":
		return "kernel"
	case name == "agentguard-cloud":
		return "cloud"
	case name == "agentguard-analytics":
		return "analytics"
	case name == "shellforge":
		return "shellforge"
	case name == "octi-pulpo":
		return "octi-pulpo"
	case strings.HasPrefix(name, "studio"):
		return "studio"
	default:
		return name
	}
}
