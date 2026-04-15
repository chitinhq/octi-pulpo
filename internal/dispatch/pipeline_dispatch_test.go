package dispatch

import (
	"testing"

	"github.com/chitinhq/octi-pulpo/internal/routing"
)

func TestPipelineRoute(t *testing.T) {
	// Ladder Forge II (2026-04-14): Mid tier now gh-actions + anthropic.
	pr := PipelineRouter{}
	decision := pr.RouteForStage("implement", 0, []routing.DriverHealth{
		{Name: "gh-actions", CircuitState: "CLOSED"},
		{Name: "anthropic", CircuitState: "CLOSED"},
	})
	if decision.Skip {
		t.Fatal("expected a route, got skip")
	}
	if decision.Driver != "gh-actions" {
		t.Errorf("driver = %s, want gh-actions (first Mid candidate)", decision.Driver)
	}
	if decision.Tier != string(routing.TierMid) {
		t.Errorf("tier = %s, want mid", decision.Tier)
	}
}

func TestPipelineRouteNoHealthyFrontier(t *testing.T) {
	pr := PipelineRouter{}
	decision := pr.RouteForStage("architect", 0, []routing.DriverHealth{
		{Name: "anthropic", CircuitState: "OPEN"},
	})
	if !decision.Skip {
		t.Error("expected skip when no healthy Frontier drivers")
	}
}
