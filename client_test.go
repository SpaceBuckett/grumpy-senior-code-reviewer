package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu             sync.Mutex
	reviewCalls    int
	rewriteCalls   int
	inFlight       int
	maxInFlight    int
	seenKeys       []string
	review         Review
	rewriteText    string
	rewriteFailure string
	reviewDelay    time.Duration
}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seenKeys = append(f.seenKeys, r.Header.Get("x-api-key"))
		f.mu.Unlock()
		var req apiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		if strings.Contains(req.System, "pre-review pass") {
			f.mu.Lock()
			f.reviewCalls++
			f.mu.Unlock()
			time.Sleep(f.reviewDelay)
			body, err := json.Marshal(f.review)
			if err != nil {
				t.Errorf("marshaling review: %v", err)
			}
			writeAPIText(w, string(body))
			return
		}
		f.mu.Lock()
		f.rewriteCalls++
		f.inFlight++
		if f.inFlight > f.maxInFlight {
			f.maxInFlight = f.inFlight
		}
		fail := f.rewriteFailure != ""
		f.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
		if fail {
			w.Write([]byte(`{"error":{"type":"api_error","message":"` + f.rewriteFailure + `"}}`))
			return
		}
		writeAPIText(w, f.rewriteText)
	}
}

const (
	fakeInputTokens  = 100
	fakeOutputTokens = 25
)

func writeAPIText(w http.ResponseWriter, text string) {
	resp := map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"usage":   map[string]int{"input_tokens": fakeInputTokens, "output_tokens": fakeOutputTokens},
	}
	json.NewEncoder(w).Encode(resp)
}

type fakeGemini struct {
	mu     sync.Mutex
	calls  int
	key    string
	path   string
	review Review
}

func (f *fakeGemini) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.key = r.Header.Get("x-goog-api-key")
		f.path = r.URL.Path
		f.mu.Unlock()
		body, err := json.Marshal(f.review)
		if err != nil {
			t.Errorf("marshaling review: %v", err)
		}
		resp := map[string]any{
			"candidates": []map[string]any{
				{"content": map[string]any{"parts": []map[string]string{{"text": string(body)}}}},
			},
			"usageMetadata": map[string]int{"promptTokenCount": 200, "candidatesTokenCount": 50},
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func (f *fakeGemini) stats() (calls int, key, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.key, f.path
}

func TestGeminiClientReview(t *testing.T) {
	fake := &fakeGemini{review: Review{Summary: "ok", Positive: "clean", Findings: severityFindings("low")}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	c := NewGeminiClient("gm-key", "gemini-test", Rubrics{})
	c.baseURL = srv.URL

	review, usage, err := c.Review(context.Background(), []Source{{Name: "a.go", Content: "package a"}}, false)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if review.Summary != "ok" || len(review.Findings) != 1 {
		t.Fatalf("unexpected review: %+v", review)
	}
	want := Usage{InputTokens: 200, OutputTokens: 50, Requests: 1}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	calls, key, path := fake.stats()
	if calls != 1 || key != "gm-key" {
		t.Fatalf("calls = %d, key = %q", calls, key)
	}
	if path != "/v1beta/models/gemini-test:generateContent" {
		t.Fatalf("path = %q", path)
	}
}

func newTestClient(t *testing.T, f *fakeAPI) (*Client, func()) {
	srv := httptest.NewServer(f.handler(t))
	c := NewClient("test-key", "test-model", Rubrics{})
	c.baseURL = srv.URL
	return c, srv.Close
}

func (f *fakeAPI) stats() (reviewCalls, rewriteCalls, maxInFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reviewCalls, f.rewriteCalls, f.maxInFlight
}

func (f *fakeAPI) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenKeys...)
}

func severityFindings(severities ...string) []Finding {
	findings := make([]Finding, len(severities))
	for i, s := range severities {
		findings[i] = Finding{File: "a.go", Severity: s, Issue: "x", Suggestion: "y"}
	}
	return findings
}

func TestReviewSinglePassRequestCount(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "s", Positive: "p", Findings: severityFindings("high", "medium")}}
	c, done := newTestClient(t, fake)
	defer done()

	review, usage, err := c.Review(context.Background(), []Source{{Name: "a.go", Content: "package a"}}, false)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	reviewCalls, rewriteCalls, _ := fake.stats()
	if reviewCalls != 1 || rewriteCalls != 0 {
		t.Fatalf("got %d review and %d rewrite calls, want 1 and 0", reviewCalls, rewriteCalls)
	}
	want := Usage{InputTokens: fakeInputTokens, OutputTokens: fakeOutputTokens, Requests: 1}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
	for _, f := range review.Findings {
		if f.Rewrite != "" {
			t.Fatalf("unexpected rewrite in single-pass mode: %+v", f)
		}
	}
}

func TestReviewTwoPassCapsFindingsAndConcurrency(t *testing.T) {
	fake := &fakeAPI{
		review:      Review{Summary: "s", Positive: "p", Findings: severityFindings("medium", "high", "medium", "low", "high", "medium", "medium", "high")},
		rewriteText: "```go\nfunc fixed() {}\n```",
	}
	c, done := newTestClient(t, fake)
	defer done()

	review, usage, err := c.Review(context.Background(), []Source{{Name: "a.go", Content: "package a"}}, true)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	wantRequests := int64(1 + maxRewriteFindings)
	wantUsage := Usage{
		InputTokens:  fakeInputTokens * wantRequests,
		OutputTokens: fakeOutputTokens * wantRequests,
		Requests:     wantRequests,
	}
	if usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", usage, wantUsage)
	}
	_, rewriteCalls, maxInFlight := fake.stats()
	if rewriteCalls != maxRewriteFindings {
		t.Errorf("got %d rewrite calls, want %d", rewriteCalls, maxRewriteFindings)
	}
	if maxInFlight > maxRewriteConcurrency {
		t.Errorf("observed %d concurrent rewrite requests, cap is %d", maxInFlight, maxRewriteConcurrency)
	}
	var rewritten, highRewritten int
	for _, f := range review.Findings {
		if f.Rewrite == "" {
			continue
		}
		rewritten++
		if f.Severity == "high" {
			highRewritten++
		}
		if f.Rewrite != "func fixed() {}" {
			t.Errorf("fences not stripped: %q", f.Rewrite)
		}
	}
	if rewritten != maxRewriteFindings {
		t.Errorf("%d findings rewritten, want %d", rewritten, maxRewriteFindings)
	}
	if highRewritten != 3 {
		t.Errorf("%d high findings rewritten, want all 3", highRewritten)
	}
}

func TestReviewRewriteFailureSurfaces(t *testing.T) {
	fake := &fakeAPI{
		review:         Review{Summary: "s", Positive: "p", Findings: severityFindings("high", "high")},
		rewriteFailure: "boom",
	}
	c, done := newTestClient(t, fake)
	defer done()

	if _, _, err := c.Review(context.Background(), []Source{{Name: "a.go", Content: "package a"}}, true); err == nil {
		t.Fatal("expected error when a rewrite request fails")
	}
}

func TestReviewHonorsContextDeadline(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "s"}, reviewDelay: 2 * time.Second}
	c, done := newTestClient(t, fake)
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := c.Review(ctx, []Source{{Name: "a.go", Content: "package a"}}, false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline error, got %v", err)
	}
}

func TestBuildSystemPromptContent(t *testing.T) {
	r, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	prompt := buildSystemPrompt(r)
	schemaKeys := []string{
		`"summary"`, `"positive"`, `"findings"`, `"file"`, `"line"`, `"severity"`,
		`"category"`, `"issue"`, `"suggestion"`, `"smell"`, `"refactoring"`,
		`"reference"`, `"rewrite"`,
	}
	for _, key := range schemaKeys {
		if !strings.Contains(prompt, key) {
			t.Errorf("prompt missing schema key %s", key)
		}
	}
	for _, want := range []string{"Long Method", "uncatalogued"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildSystemPromptStaysCompact(t *testing.T) {
	r, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	if n := len(buildSystemPrompt(r)); n > 20000 {
		t.Fatalf("prompt is %d characters, want at most 20000", n)
	}
}
