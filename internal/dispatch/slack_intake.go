package dispatch

import (
	"fmt"
	"strings"
	"time"
)

// MessageType classifies incoming Slack messages.
type MessageType string

const (
	MessageTypePipelineCmd MessageType = "pipeline_cmd"
	MessageTypeBrief       MessageType = "brief"
)

// ClassifyMessage determines whether a Slack message is a pipeline command
// or a brief that should be routed to the CTO agent for decomposition.
func ClassifyMessage(text string) MessageType {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if strings.HasPrefix(trimmed, "pipeline") {
		return MessageTypePipelineCmd
	}
	return MessageTypeBrief
}

// FormatBriefIssue creates a GitHub Issue title and body from a Slack brief.
// The issue is labeled stage:architect so the pipeline picks it up.
func FormatBriefIssue(text, slackUserID string) (title, body string) {
	// Use first 70 chars of the brief as the title
	title = text
	if len(title) > 70 {
		title = title[:67] + "..."
	}

	body = fmt.Sprintf(`## Slack brief

**From:** <@%s>
**Received:** %s

### Brief

%s

### Instructions

This issue was created from a Slack brief. The Architect agent should:
1. Decompose this into implementable tickets
2. Create sub-issues with specs and acceptance criteria
3. Label sub-issues as `+"`stage:implement`"+`

---
*Auto-created by Octi Pulpo pipeline intake*
`, slackUserID, time.Now().UTC().Format("2006-01-02 15:04 UTC"), text)

	return title, body
}