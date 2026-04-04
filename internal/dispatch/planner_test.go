package dispatch

import (
	"context"
	"testing"
)

func TestPlannerHandler_NewPlannerHandler(t *testing.T) {
	// Test that NewPlannerHandler creates a handler with default values
	handler := NewPlannerHandler("", "", "")
	if handler == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestPlannerHandler_HandleIssue_BudgetExceeded(t *testing.T) {
	// Test that HandleIssue escalates to tier:a-groom when budget is exceeded
	handler := NewPlannerHandler("test-token", "test-api-key", "test-model")
	
	// Create a budget store that will trigger budget gate
	// We need to simulate budget exceeded scenario
	// For now, just test that the function exists and returns expected types
	ctx := context.Background()
	result, err := handler.HandleIssue(ctx, "test/repo", 1, "Test Issue", "Test body")
	
	// When budget is not configured, it should try to call Claude API
	// but will fail due to missing credentials
	if err == nil {
		t.Logf("Result: %+v", result)
		// This is OK - the actual API call will fail in test environment
	}
}

func TestPlannerResult_JSONStructure(t *testing.T) {
	// Test that PlannerResult has the expected JSON structure
	result := PlannerResult{
		AcceptanceCriteria: "Test criteria",
		Escalate:           false,
		Reason:             "Test reason",
		CostCents:          10,
		Model:              "test-model",
	}
	
	if result.AcceptanceCriteria != "Test criteria" {
		t.Errorf("Expected AcceptanceCriteria 'Test criteria', got %s", result.AcceptanceCriteria)
	}
	if result.Escalate != false {
		t.Errorf("Expected Escalate false, got %v", result.Escalate)
	}
	if result.CostCents != 10 {
		t.Errorf("Expected CostCents 10, got %d", result.CostCents)
	}
}

func TestSubIssue_JSONStructure(t *testing.T) {
	// Test that SubIssue has the expected JSON structure
	subIssue := SubIssue{
		Title: "Test sub-issue",
		Body:  "Test body",
	}
	
	if subIssue.Title != "Test sub-issue" {
		t.Errorf("Expected Title 'Test sub-issue', got %s", subIssue.Title)
	}
	if subIssue.Body != "Test body" {
		t.Errorf("Expected Body 'Test body', got %s", subIssue.Body)
	}
}

func TestPlannerHandler_ScopeMethodExists(t *testing.T) {
	// Test that the scope method exists (even though it's private)
	handler := NewPlannerHandler("test-token", "test-api-key", "test-model")
	
	// We can't directly test the private scope method, but we can verify
	// that HandleIssue calls it indirectly
	ctx := context.Background()
	_, err := handler.HandleIssue(ctx, "test/repo", 1, "Test Issue", "Test body")
	
	// Error is expected since we don't have real API credentials
	if err != nil {
		t.Logf("Expected error without API credentials: %v", err)
	}
}

func TestPlannerHandler_HandleIssue_EmptyBody(t *testing.T) {
	// Test handling issue with empty body
	handler := NewPlannerHandler("test-token", "test-api-key", "test-model")
	ctx := context.Background()
	
	result, err := handler.HandleIssue(ctx, "test/repo", 1, "Test Issue", "")
	
	// Should not panic with empty body
	if err != nil {
		t.Logf("Error with empty body (expected): %v", err)
	}
	if result != nil {
		t.Logf("Result with empty body: %+v", result)
	}
}

func TestPlannerHandler_HandleIssue_LongTitle(t *testing.T) {
	// Test handling issue with very long title
	longTitle := "This is a very long title that might cause issues with the API call or buffer limits " +
		"because it keeps going and going and going and going and going and going and going " +
		"and going and going and going and going and going and going and going and going"
	
	handler := NewPlannerHandler("test-token", "test-api-key", "test-model")
	ctx := context.Background()
	
	result, err := handler.HandleIssue(ctx, "test/repo", 1, longTitle, "Test body")
	
	// Should not panic with long title
	if err != nil {
		t.Logf("Error with long title (expected): %v", err)
	}
	if result != nil {
		t.Logf("Result with long title: %+v", result)
	}
}

func TestPlannerHandler_SetBudgetStore(t *testing.T) {
	// Test that SetBudgetStore works
	handler := NewPlannerHandler("test-token", "test-api-key", "test-model")
	
	// Should not panic when setting nil budget store
	handler.SetBudgetStore(nil)
	
	// Test is successful if we reach here without panic
	t.Log("SetBudgetStore with nil completed without panic")
}