package dispatch

// LiveRepos is the authoritative list of repos that the dispatch layer
// routes to. Every agent name referenced in DefaultChains, DefaultRules,
// NewSignalWatcher.repoSeniors, and Brain.srForRepo must derive its
// prefix from this list.
//
// Squad-era entries (cloud, octi-pulpo, studio, office-sim, analytics,
// platform, marketing, design, site, qa, ops) were excised in octi#271
// Phase 1 when the org collapsed to per-repo routing. Regrowth is
// blocked by TestNoFossilAgentsInChains + TestNoFossilTimersInRules in
// fossil_regression_test.go — both keyed to this const.
//
// Adding a new repo: update this slice, add the SR mapping in
// Brain.srForRepo and NewSignalWatcher.repoSeniors, and re-run the
// regression tests.
var LiveRepos = []string{
	"kernel",
	"shellforge",
	"clawta",
	"sentinel",
	"llmint",
	"octi",
	"workspace",
	"ganglia",
}
