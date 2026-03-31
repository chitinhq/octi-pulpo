package routing

import "testing"

func TestModelTierForStage(t *testing.T) {
	tests := []struct {
		stage string
		want  ModelTier
	}{
		{"architect", TierFrontier},
		{"implement", TierMid},
		{"qa", TierLight},
		{"review", TierMid},
		{"release", TierNone},
	}
	for _, tt := range tests {
		got := TierForStage(tt.stage)
		if got != tt.want {
			t.Errorf("TierForStage(%q) = %v, want %v", tt.stage, got, tt.want)
		}
	}
}

func TestDriversForTier(t *testing.T) {
	drivers := DriversForTier(TierFrontier)
	found := false
	for _, d := range drivers {
		if d == "claude-code" {
			found = true
		}
	}
	if !found {
		t.Error("expected claude-code in Frontier drivers")
	}
}

func TestTierEscalation(t *testing.T) {
	got := TierForStageWithRisk("review", 45)
	if got != TierFrontier {
		t.Errorf("high-risk review got %v, want Frontier", got)
	}
	got = TierForStageWithRisk("review", 10)
	if got != TierMid {
		t.Errorf("low-risk review got %v, want Mid", got)
	}
}
