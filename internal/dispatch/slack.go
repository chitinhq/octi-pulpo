package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/chitinhq/octi-pulpo/internal/pipeline"
	"github.com/chitinhq/octi-pulpo/internal/routing"
)

// Notifier posts messages to Slack via webhook.
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// NewNotifier creates a new Slack notifier.
func NewNotifier(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{},
	}
}

// Enabled returns true if the notifier is configured.
func (n *Notifier) Enabled() bool {
	return n.webhookURL != ""
}

// PostPipelineDashboard sends a pipeline status dashboard to Slack using Block Kit.
func (n *Notifier) PostPipelineDashboard(
	ctx context.Context,
	depths map[pipeline.Stage]int,
	sessions map[pipeline.Stage]int,
	budgets []routing.DriverHealth,
	bp pipeline.BackpressureAction,
) error {
	if !n.Enabled() {
		return nil
	}

	blocks := FormatPipelineDashboard(depths, sessions, budgets, bp)

	payload := map[string]interface{}{
		"blocks": blocks,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pipeline dashboard: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %d", resp.StatusCode)
	}
	return nil
}