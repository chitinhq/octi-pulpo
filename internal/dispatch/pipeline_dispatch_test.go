package dispatch

import (
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func TestPipelineRoute(t *testing.T) {
	pr := PipelineRouter{}
	decision := pr.RouteForStage("implement", 0, []routing.DriverHealth{
		{Name: "copilot", CircuitState: "CLOSED"},
		{Name: "claude-code", CircuitState: "CLOSED"},
		{Name: "codex", CircuitState: "CLOSED"},
	})
	if decision.Skip {
		t.Fatal("expected a route, got skip")
	}
	if decision.Driver != "copilot" {
		t.Errorf("driver = %s, want copilot (cheapest Mid)", decision.Driver)
	}
	if decision.Tier != string(routing.TierMid) {
		t.Errorf("tier = %s, want mid", decision.Tier)
	}
}

func TestPipelineRouteNoHealthyFrontier(t *testing.T) {
	pr := PipelineRouter{}
	decision := pr.RouteForStage("architect", 0, []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "OPEN"},
		{Name: "copilot", CircuitState: "OPEN"},
	})
	if !decision.Skip {
		t.Error("expected skip when no healthy Frontier drivers")
	}
}
