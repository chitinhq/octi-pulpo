package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/pipeline"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	pollInterval := 30 * time.Second
	router := routing.NewRouter("")

	scalerCfg := pipeline.ScalerConfig{
		MinSessions: map[pipeline.Stage]int{
			pipeline.StageArchitect: 0, pipeline.StageImplement: 1,
			pipeline.StageQA: 1, pipeline.StageReview: 1,
		},
		MaxSessions: map[pipeline.Stage]int{
			pipeline.StageArchitect: 3, pipeline.StageImplement: 8,
			pipeline.StageQA: 4, pipeline.StageReview: 3,
		},
		ScaleUpThreshold: map[pipeline.Stage]int{
			pipeline.StageArchitect: 3, pipeline.StageImplement: 5,
			pipeline.StageQA: 3, pipeline.StageReview: 3,
		},
	}
	scaler := pipeline.NewScaler(scalerCfg)

	log.Println("[octi-pipeline] starting pipeline controller")
	log.Printf("[octi-pipeline] poll interval: %s", pollInterval)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[octi-pipeline] shutting down")
			return
		case <-ticker.C:
			runControlLoop(router, scaler)
		}
	}
}

func runControlLoop(router *routing.Router, scaler *pipeline.Scaler) {
	depths := readQueueDepths()

	bp := pipeline.EvaluateBackpressure(depths)
	if bp.PauseStage != "" || bp.ThrottleStage != "" {
		log.Printf("[octi-pipeline] backpressure: %s", bp.Reason)
	}

	desired := scaler.DesiredSessions(depths, bp)

	if pipeline.IsStarving(depths) {
		log.Println("[octi-pipeline] pipeline starving — signal architect to generate work")
	}

	log.Printf("[octi-pipeline] depths: arch=%d impl=%d qa=%d rev=%d | desired: arch=%d impl=%d qa=%d rev=%d",
		depths[pipeline.StageArchitect], depths[pipeline.StageImplement],
		depths[pipeline.StageQA], depths[pipeline.StageReview],
		desired[pipeline.StageArchitect], desired[pipeline.StageImplement],
		desired[pipeline.StageQA], desired[pipeline.StageReview],
	)

	health := router.AllHealth()
	for stage, count := range desired {
		if count > 0 {
			tier := routing.TierForStage(string(stage))
			log.Printf("[octi-pipeline] stage %s: need %d sessions at %s tier (drivers: %v)",
				stage, count, tier, driversAvailable(health, tier))
		}
	}
}

func readQueueDepths() map[pipeline.Stage]int {
	return map[pipeline.Stage]int{
		pipeline.StageArchitect: 0, pipeline.StageImplement: 0,
		pipeline.StageQA: 0, pipeline.StageReview: 0,
	}
}

func driversAvailable(health []routing.DriverHealth, tier routing.ModelTier) []string {
	candidates := routing.DriversForTier(tier)
	healthMap := make(map[string]bool)
	for _, h := range health {
		if h.CircuitState != "OPEN" {
			healthMap[h.Name] = true
		}
	}
	var available []string
	for _, c := range candidates {
		if healthMap[c] {
			available = append(available, c)
		}
	}
	return available
}
