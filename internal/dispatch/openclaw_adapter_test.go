package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---- Name ----

func TestOpenClawAdapterName(t *testing.T) {
	a := NewOpenClawAdapter("", "", "", "")
	if got := a.Name(); got != "openclaw" {
		t.Errorf("Name(): want openclaw, got %s", got)
	}
}

// ---- CanAccept ----

func TestOpenClawCanAcceptNilTask(t *testing.T) {
	a := NewOpenClawAdapter("http://localhost", "tok", "!room", "@bot:local")
	if a.CanAccept(nil) {
		t.Error("CanAccept(nil): want false, got true")
	}
}

func TestOpenClawCanAcceptNoCredentials(t *testing.T) {
	a := NewOpenClawAdapter("http://localhost", "", "", "")
	task := &Task{Type: "research"}
	if a.CanAccept(task) {
		t.Error("CanAccept with no creds: want false, got true")
	}
}

func TestOpenClawCanAcceptSupportedTypes(t *testing.T) {
	a := NewOpenClawAdapter("http://localhost", "tok", "!room", "@bot:local")
	for _, typ := range []string{"research", "triage", "qa", "prompt_config", "tool_addition"} {
		task := &Task{Type: typ}
		if !a.CanAccept(task) {
			t.Errorf("CanAccept(%s): want true, got false", typ)
		}
	}
}

func TestOpenClawCanAcceptUnsupportedTypes(t *testing.T) {
	a := NewOpenClawAdapter("http://localhost", "tok", "!room", "@bot:local")
	for _, typ := range []string{"code-gen", "bugfix", "pr-review", ""} {
		task := &Task{Type: typ}
		if a.CanAccept(task) {
			t.Errorf("CanAccept(%s): want false, got true", typ)
		}
	}
}

// ---- sendMessage ----

func TestSendMessageSuccess(t *testing.T) {
	var gotAuth string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(200)
		w.Write([]byte(`{"event_id":"$abc"}`))
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "test-token", "!room:local", "@bot:local")
	err := a.sendMessage(context.Background(), "txn-1", "hello")
	if err != nil {
		t.Fatalf("sendMessage: unexpected error: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth header: want 'Bearer test-token', got %q", gotAuth)
	}
	if gotBody["body"] != "hello" {
		t.Errorf("body: want 'hello', got %q", gotBody["body"])
	}
}

func TestSendMessageHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"errcode":"M_FORBIDDEN"}`))
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "bad-token", "!room:local", "@bot:local")
	err := a.sendMessage(context.Background(), "txn-1", "hello")
	if err == nil {
		t.Fatal("sendMessage: expected error on 403, got nil")
	}
}

// ---- checkForBotMessage ----

func TestCheckForBotMessageFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"chunk": []map[string]interface{}{
				{
					"type":   "m.room.message",
					"sender": "@bot:local",
					"content": map[string]string{
						"body":    "task completed",
						"msgtype": "m.text",
					},
				},
			},
			"end": "t2_token",
		})
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	msg, found, nextToken, err := a.checkForBotMessage(context.Background(), "t1_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if msg != "task completed" {
		t.Errorf("message: want 'task completed', got %q", msg)
	}
	if nextToken != "t2_token" {
		t.Errorf("nextToken: want 't2_token', got %q", nextToken)
	}
}

func TestCheckForBotMessageSkipsPairing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"chunk": []map[string]interface{}{
				{
					"type":   "m.room.message",
					"sender": "@bot:local",
					"content": map[string]string{
						"body":    "Pairing code: ABC123",
						"msgtype": "m.text",
					},
				},
			},
			"end": "t2",
		})
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	_, found, _, err := a.checkForBotMessage(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false for pairing message")
	}
}

func TestCheckForBotMessageNonTransientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"errcode":"M_UNKNOWN_TOKEN"}`))
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "bad-tok", "!room:local", "@bot:local")
	_, _, _, err := a.checkForBotMessage(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestCheckForBotMessageAdvancesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"chunk": []map[string]interface{}{},
			"end":   "t3_advanced",
		})
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	_, _, nextToken, err := a.checkForBotMessage(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nextToken != "t3_advanced" {
		t.Errorf("nextToken: want 't3_advanced', got %q", nextToken)
	}
}

// ---- getSyncToken ----

func TestGetSyncTokenSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"start": "s123"})
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	tok, err := a.getSyncToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "s123" {
		t.Errorf("token: want 's123', got %q", tok)
	}
}

func TestGetSyncTokenHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	_, err := a.getSyncToken(context.Background())
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

// ---- Dispatch (integration) ----

func TestDispatchTimeout(t *testing.T) {
	// Server that accepts send but never returns a bot message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			w.WriteHeader(200)
			w.Write([]byte(`{"event_id":"$1"}`))
			return
		}
		// GET /messages — always return empty
		json.NewEncoder(w).Encode(map[string]interface{}{
			"chunk": []map[string]interface{}{},
			"start": "s1",
			"end":   "s2",
		})
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	a.timeout = 500 * time.Millisecond

	task := &Task{ID: "test-1", Type: "research", Prompt: "test prompt"}
	_, err := a.Dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDispatchSuccess(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			w.WriteHeader(200)
			w.Write([]byte(`{"event_id":"$1"}`))
			return
		}
		callCount++
		// First poll: empty. Second poll: bot response.
		if callCount <= 2 {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"chunk": []map[string]interface{}{},
				"start": "s1",
				"end":   "s2",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"chunk": []map[string]interface{}{
					{
						"type":   "m.room.message",
						"sender": "@bot:local",
						"content": map[string]string{
							"body":    "done: all good",
							"msgtype": "m.text",
						},
					},
				},
				"end": "s3",
			})
		}
	}))
	defer srv.Close()

	a := NewOpenClawAdapter(srv.URL, "tok", "!room:local", "@bot:local")
	a.timeout = 5 * time.Second

	task := &Task{ID: "test-2", Type: "research", Prompt: "test prompt"}
	result, err := a.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status: want 'completed', got %q", result.Status)
	}
	if result.Output != "done: all good" {
		t.Errorf("output: want 'done: all good', got %q", result.Output)
	}
}

// ---- isNonTransientError ----

func TestIsNonTransientError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"matrix messages failed (401): unauthorized", true},
		{"matrix messages failed (403): forbidden", true},
		{"matrix messages failed (404): not found", true},
		{"matrix messages failed (500): internal error", false},
		{"connection refused", false},
	}
	for _, tt := range tests {
		got := isNonTransientError(fmt.Errorf(tt.msg))
		if got != tt.want {
			t.Errorf("isNonTransientError(%q): want %v, got %v", tt.msg, tt.want, got)
		}
	}
}
