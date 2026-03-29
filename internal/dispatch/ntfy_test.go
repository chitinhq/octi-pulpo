package dispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNtfyNotifier_Enabled(t *testing.T) {
	if NewNtfyNotifier("", "").Enabled() {
		t.Fatal("empty config should not be enabled")
	}
	if NewNtfyNotifier("https://ntfy.sh", "").Enabled() {
		t.Fatal("missing topic should not be enabled")
	}
	if NewNtfyNotifier("", "my-topic").Enabled() {
		t.Fatal("missing baseURL should not be enabled")
	}
	if !NewNtfyNotifier("https://ntfy.sh", "my-topic").Enabled() {
		t.Fatal("fully configured notifier should be enabled")
	}
}

func TestNtfyNotifier_NoopWhenDisabled(t *testing.T) {
	ctx := context.Background()
	n := NewNtfyNotifier("", "")

	if err := n.Post(ctx, "title", "msg", NtfyPriorityDefault); err != nil {
		t.Fatalf("Post on disabled notifier: %v", err)
	}
	if err := n.PostDriverAlert(ctx, "codex", 5); err != nil {
		t.Fatalf("PostDriverAlert on disabled notifier: %v", err)
	}
	if err := n.PostAllDriversDown(ctx, "all down"); err != nil {
		t.Fatalf("PostAllDriversDown on disabled notifier: %v", err)
	}
}

func TestNtfyNotifier_Post(t *testing.T) {
	var gotTitle, gotPriority, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("X-Title")
		gotPriority = r.Header.Get("X-Priority")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNtfyNotifier(srv.URL, "test-topic")

	if err := n.Post(ctx, "Alert!", "Something happened", NtfyPriorityHigh); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotTitle != "Alert!" {
		t.Errorf("X-Title = %q, want %q", gotTitle, "Alert!")
	}
	if gotPriority != "4" {
		t.Errorf("X-Priority = %q, want %q", gotPriority, "4")
	}
	if gotBody != "Something happened" {
		t.Errorf("body = %q, want %q", gotBody, "Something happened")
	}
}

func TestNtfyNotifier_DefaultPriorityOmitsHeader(t *testing.T) {
	var gotPriority string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPriority = r.Header.Get("X-Priority")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNtfyNotifier(srv.URL, "test-topic")
	_ = n.Post(ctx, "t", "m", NtfyPriorityDefault)

	if gotPriority != "" {
		t.Errorf("expected no X-Priority header for default priority, got %q", gotPriority)
	}
}

func TestNtfyNotifier_PostDriverAlert(t *testing.T) {
	var gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("X-Title")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNtfyNotifier(srv.URL, "test-topic")

	if err := n.PostDriverAlert(ctx, "codex", 63); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotTitle != "Driver Alert: codex" {
		t.Errorf("title = %q, want %q", gotTitle, "Driver Alert: codex")
	}
	if gotBody == "" {
		t.Error("expected non-empty body")
	}
}

func TestNtfyNotifier_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx := context.Background()
	n := NewNtfyNotifier(srv.URL, "test-topic")

	err := n.Post(ctx, "t", "m", NtfyPriorityDefault)
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
}
