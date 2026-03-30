package org

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// --- inferRole ---

func TestInferRole(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"octi-pulpo-em", "EM"},
		{"kernel-em", "EM"},
		{"kernel-sr", "SR"},
		{"kernel-jr", "JR"},
		{"kernel-qa", "QA"},
		{"kernel-pl", "PL"},
		{"kernel-arch", "Arch"},
		{"infra-director", "Director"},
		{"jared-director-ops", "Director"},
		{"goose", ""},
		{"claude-code", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := inferRole(tc.name)
		if got != tc.want {
			t.Errorf("inferRole(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// --- inferReportsTo ---

func TestInferReportsTo(t *testing.T) {
	cases := []struct {
		name  string
		role  string
		squad string
		want  string
	}{
		{"infra-director", "Director", "", "jared"},
		{"infra-director", "Director", "infra", "jared"},
		{"kernel-em", "EM", "kernel", "director"},
		{"kernel-sr", "SR", "kernel", "kernel-em"},
		{"kernel-jr", "JR", "kernel", "kernel-em"},
		{"lone-wolf", "SR", "", ""},
		{"lone-wolf", "", "", ""},
	}
	for _, tc := range cases {
		got := inferReportsTo(tc.name, tc.role, tc.squad)
		if got != tc.want {
			t.Errorf("inferReportsTo(%q, %q, %q) = %q, want %q",
				tc.name, tc.role, tc.squad, got, tc.want)
		}
	}
}

// --- loadSquadRoles ---

func TestLoadSquadRoles_Empty(t *testing.T) {
	roles := loadSquadRoles("")
	if len(roles) != 0 {
		t.Errorf("expected empty map for empty squadsDir, got %v", roles)
	}
}

func TestLoadSquadRoles_MissingDir(t *testing.T) {
	roles := loadSquadRoles("/no/such/dir/here")
	if len(roles) != 0 {
		t.Errorf("expected empty map for missing dir, got %v", roles)
	}
}

func TestLoadSquadRoles_ParsesRoles(t *testing.T) {
	dir := t.TempDir()

	state := squadState{
		Squad: "kernel",
		Agents: map[string]squadAgentState{
			"kernel-em": {Role: "EM", Status: "active"},
			"kernel-sr": {Role: "SR", Status: "active"},
			"kernel-qa": {Role: "QA", Status: "idle"},
		},
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "kernel.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	roles := loadSquadRoles(dir)
	if roles["kernel-em"] != "EM" {
		t.Errorf("kernel-em: got %q, want EM", roles["kernel-em"])
	}
	if roles["kernel-sr"] != "SR" {
		t.Errorf("kernel-sr: got %q, want SR", roles["kernel-sr"])
	}
	if roles["kernel-qa"] != "QA" {
		t.Errorf("kernel-qa: got %q, want QA", roles["kernel-qa"])
	}
}

func TestLoadSquadRoles_SkipsNonJSON(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not json"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0o644)

	roles := loadSquadRoles(dir)
	if len(roles) != 0 {
		t.Errorf("expected empty map, got %v", roles)
	}
}

func TestLoadSquadRoles_SkipsEmptyRoles(t *testing.T) {
	dir := t.TempDir()

	state := squadState{
		Squad: "cloud",
		Agents: map[string]squadAgentState{
			"cloud-em": {Role: "", Status: "active"},
		},
	}
	raw, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(dir, "cloud.json"), raw, 0o644)

	roles := loadSquadRoles(dir)
	if _, ok := roles["cloud-em"]; ok {
		t.Error("expected agent with empty role to be excluded from map")
	}
}

func TestLoadSquadRoles_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	for _, td := range []struct {
		file  string
		squad string
		agent string
		role  string
	}{
		{"kernel.json", "kernel", "kernel-em", "EM"},
		{"cloud.json", "cloud", "cloud-sr", "SR"},
	} {
		state := squadState{
			Squad: td.squad,
			Agents: map[string]squadAgentState{
				td.agent: {Role: td.role},
			},
		}
		raw, _ := json.Marshal(state)
		os.WriteFile(filepath.Join(dir, td.file), raw, 0o644)
	}

	roles := loadSquadRoles(dir)
	if roles["kernel-em"] != "EM" {
		t.Errorf("kernel-em: got %q, want EM", roles["kernel-em"])
	}
	if roles["cloud-sr"] != "SR" {
		t.Errorf("cloud-sr: got %q, want SR", roles["cloud-sr"])
	}
}

// --- ImportFromSchedule (requires Redis) ---

func testOrgSetup(t *testing.T) (*OrgStore, context.Context) {
	t.Helper()
	redisURL := os.Getenv("OCTI_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Skipf("skipping: cannot parse redis URL: %v", err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis not available: %v", err)
	}
	ns := "octi-test-import-" + t.Name()
	cleanup := func() {
		keys, _ := rdb.Keys(ctx, ns+":*").Result()
		if len(keys) > 0 {
			rdb.Del(ctx, keys...)
		}
	}
	cleanup()
	t.Cleanup(func() { cleanup(); rdb.Close() })
	return NewOrgStore(rdb, ns), ctx
}

func TestImportFromSchedule_Basic(t *testing.T) {
	store, ctx := testOrgSetup(t)

	scheduleData := scheduleJSON{
		Agents: map[string]scheduleAgent{
			"kernel-em": {Driver: "claude-code", Squad: "kernel", Box: "jared", Enabled: true},
			"kernel-sr": {Driver: "claude-code", Squad: "kernel", Box: "jared", Enabled: true},
			"disabled":  {Driver: "goose", Squad: "cloud", Box: "jared", Enabled: false},
		},
	}
	raw, _ := json.Marshal(scheduleData)

	dir := t.TempDir()
	schedulePath := filepath.Join(dir, "schedule.json")
	if err := os.WriteFile(schedulePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	count, err := ImportFromSchedule(ctx, store, schedulePath, "")
	if err != nil {
		t.Fatalf("ImportFromSchedule: %v", err)
	}
	// 2 enabled + 1 jared root
	if count != 3 {
		t.Errorf("expected count=3, got %d", count)
	}

	// Jared should always be present as board root.
	jared, err := store.Get(ctx, "jared")
	if err != nil {
		t.Fatalf("Get jared: %v", err)
	}
	if jared.Role != "Board" {
		t.Errorf("jared role: got %q, want Board", jared.Role)
	}

	// Disabled agent should not be stored.
	_, err = store.Get(ctx, "disabled")
	if err == nil {
		t.Error("expected disabled agent to be absent, but Get succeeded")
	}
}

func TestImportFromSchedule_MissingFile(t *testing.T) {
	store, ctx := testOrgSetup(t)
	_, err := ImportFromSchedule(ctx, store, "/no/such/schedule.json", "")
	if err == nil {
		t.Error("expected error for missing schedule file, got nil")
	}
}

func TestImportFromSchedule_InvalidJSON(t *testing.T) {
	store, ctx := testOrgSetup(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	os.WriteFile(path, []byte("{not valid json"), 0o644)
	_, err := ImportFromSchedule(ctx, store, path, "")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestImportFromSchedule_RoleOverrideFromSquadState(t *testing.T) {
	store, ctx := testOrgSetup(t)

	scheduleData := scheduleJSON{
		Agents: map[string]scheduleAgent{
			"kernel-em": {Driver: "claude-code", Squad: "kernel", Box: "jared", Enabled: true},
		},
	}
	schedRaw, _ := json.Marshal(scheduleData)

	// Squad state overrides inferred role for kernel-em.
	state := squadState{
		Squad: "kernel",
		Agents: map[string]squadAgentState{
			"kernel-em": {Role: "EM-Lead", Status: "active"},
		},
	}
	stateRaw, _ := json.Marshal(state)

	dir := t.TempDir()
	schedulePath := filepath.Join(dir, "schedule.json")
	squadsDir := filepath.Join(dir, "squads")
	os.MkdirAll(squadsDir, 0o755)
	os.WriteFile(schedulePath, schedRaw, 0o644)
	os.WriteFile(filepath.Join(squadsDir, "kernel.json"), stateRaw, 0o644)

	_, err := ImportFromSchedule(ctx, store, schedulePath, squadsDir)
	if err != nil {
		t.Fatalf("ImportFromSchedule: %v", err)
	}

	agent, err := store.Get(ctx, "kernel-em")
	if err != nil {
		t.Fatalf("Get kernel-em: %v", err)
	}
	if agent.Role != "EM-Lead" {
		t.Errorf("expected role=EM-Lead (squad state override), got %q", agent.Role)
	}
}

// --- PrintTree (requires Redis) ---

func TestPrintTree_Basic(t *testing.T) {
	store, ctx := testOrgSetup(t)

	agents := []Agent{
		{Name: "jared", Role: "Board"},
		{Name: "director", Role: "Director", ReportsTo: "jared"},
		{Name: "kernel-em", Squad: "kernel", Role: "EM", ReportsTo: "director"},
		{Name: "kernel-sr", Squad: "kernel", Role: "SR", ReportsTo: "kernel-em"},
	}
	for _, a := range agents {
		if err := store.Put(ctx, a); err != nil {
			t.Fatalf("Put %s: %v", a.Name, err)
		}
	}

	tree, err := PrintTree(ctx, store)
	if err != nil {
		t.Fatalf("PrintTree: %v", err)
	}
	if !strings.HasPrefix(tree, "jared") {
		t.Errorf("tree should start with 'jared', got:\n%s", tree)
	}
	if !strings.Contains(tree, "director") {
		t.Error("tree missing director")
	}
	if !strings.Contains(tree, "kernel-em") {
		t.Error("tree missing kernel-em")
	}
	if !strings.Contains(tree, "kernel-sr") {
		t.Error("tree missing kernel-sr")
	}
}

func TestPrintTree_Empty(t *testing.T) {
	store, ctx := testOrgSetup(t)

	// No agents stored — jared node is absent, walk from jared produces just that line.
	tree, err := PrintTree(ctx, store)
	if err != nil {
		t.Fatalf("PrintTree on empty store: %v", err)
	}
	// Empty store: no agents, nothing under jared, output should be "jared\n"
	if tree != "jared\n" {
		t.Errorf("empty store: expected 'jared\\n', got %q", tree)
	}
}
