package dispatch

import (
	"fmt"
	"strings"
	"time"
)

type MessageType string

const (
	MessageTypePipelineCmd MessageType = "pipeline_cmd"
	MessageTypeBrief       MessageType = "brief"
)

func ClassifyMessage(text string) MessageType {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if strings.HasPrefix(trimmed, "pipeline") {
		return MessageTypePipelineCmd
	}
	return MessageTypeBrief
}

func FormatBriefIssue(text, slackUserID string) (title, body string) {
	title = text
	if len(title) > 70 {
		title = title[:67] + "..."
	}

	body = fmt.Sprintf("## Slack brief\n\n**From:** <@%s>\n**Received:** %s\n\n### Brief\n\n%s\n\n### Instructions\n\nThis issue was created from a Slack brief. The Architect agent should:\n1. Decompose this into implementable tickets\n2. Create sub-issues with specs and acceptance criteria\n3. Label sub-issues as `stage:implement`\n\n---\n*Auto-created by Octi Pulpo pipeline intake*\n",
		slackUserID, time.Now().UTC().Format("2006-01-02 15:04 UTC"), text)

	return title, body
}
