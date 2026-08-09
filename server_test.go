package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, fake *fakeAPI) (http.Handler, func()) {
	srv := httptest.NewServer(fake.handler(t))
	s := newServer("test-key", "", "test-model", "test-gemini-model", 5*time.Second)
	s.baseURL = srv.URL
	return s.routes(), srv.Close
}

func TestServeIndexPage(t *testing.T) {
	handler, done := newTestServer(t, &fakeAPI{})
	defer done()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<textarea", "GrumpySenior", "/review"} {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}

func TestReviewHandlerHappyPath(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "looks fine", Positive: "clear names", Findings: severityFindings("low")}}
	handler, done := newTestServer(t, fake)
	defer done()

	body := `{"code":"package a","language":"go","rewrite":false}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	var review Review
	if err := json.Unmarshal(rec.Body.Bytes(), &review); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if review.Summary != "looks fine" || len(review.Findings) != 1 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestReviewHandlerRejectsOversizedBody(t *testing.T) {
	handler, done := newTestServer(t, &fakeAPI{})
	defer done()

	huge := `{"code":"` + strings.Repeat("a", maxReviewBodyBytes+1) + `","language":"go"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(huge)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestReviewHandlerRateLimitsPerIP(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "s", Positive: "p"}}
	handler, done := newTestServer(t, fake)
	defer done()

	send := func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(`{"code":"package a"}`))
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < rateLimitPerMinute; i++ {
		if code := send("10.0.0.1:5000"); code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, code)
		}
	}
	if code := send("10.0.0.1:5000"); code != http.StatusTooManyRequests {
		t.Fatalf("request %d status = %d, want 429", rateLimitPerMinute+1, code)
	}
	if code := send("10.0.0.2:5000"); code != http.StatusOK {
		t.Fatalf("other IP status = %d, want 200", code)
	}
}

func TestReviewHandlerAPIKeySelection(t *testing.T) {
	cases := []struct {
		name       string
		serverKey  string
		headerKey  string
		wantStatus int
		wantKey    string
	}{
		{"header key overrides server key", "server-key", "user-key", http.StatusOK, "user-key"},
		{"falls back to server key", "server-key", "", http.StatusOK, "server-key"},
		{"blank header falls back", "server-key", "   ", http.StatusOK, "server-key"},
		{"no key anywhere is unauthorized", "", "", http.StatusUnauthorized, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeAPI{review: Review{Summary: "s", Positive: "p"}}
			srv := httptest.NewServer(fake.handler(t))
			defer srv.Close()
			s := newServer(c.serverKey, "", "test-model", "test-gemini-model", 5*time.Second)
			s.baseURL = srv.URL

			req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(`{"code":"package a"}`))
			if c.headerKey != "" {
				req.Header.Set("X-API-Key", c.headerKey)
			}
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
			keys := fake.keys()
			if c.wantKey == "" {
				if len(keys) != 0 {
					t.Fatalf("expected no upstream calls, API saw keys %v", keys)
				}
				return
			}
			if len(keys) != 1 || keys[0] != c.wantKey {
				t.Fatalf("API saw keys %v, want exactly [%q]", keys, c.wantKey)
			}
		})
	}
}

func TestReviewHandlerAttachesSnippets(t *testing.T) {
	fake := &fakeAPI{review: Review{
		Summary:  "s",
		Positive: "p",
		Findings: []Finding{{File: "input.go", Line: "2", Severity: "high", Issue: "x", Suggestion: "y"}},
	}}
	handler, done := newTestServer(t, fake)
	defer done()

	body := `{"code":"package a\nfunc leaky() {}\nvar x int","language":"go"}`
	req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp reviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	s := resp.Findings[0].Snippet
	if s == nil {
		t.Fatal("expected a snippet on the finding")
	}
	if s.Lines[s.HighlightIndex] != "func leaky() {}" {
		t.Fatalf("highlighted line = %q, snippet = %+v", s.Lines[s.HighlightIndex], s)
	}
}

func TestReviewHandlerProviderSelection(t *testing.T) {
	cases := []struct {
		name            string
		envAnthropic    string
		envGemini       string
		headerAnthropic string
		headerGemini    string
		wantProvider    string
		wantModel       string
	}{
		{"gemini header only", "", "", "", "user-gm", "gemini", "test-gemini-model"},
		{"anthropic wins when both headers set", "", "", "user-ant", "user-gm", "anthropic", "test-model"},
		{"env gemini fallback", "", "env-gm", "", "", "gemini", "test-gemini-model"},
		{"env anthropic preferred over env gemini", "env-ant", "env-gm", "", "", "anthropic", "test-model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			anthropicFake := &fakeAPI{review: Review{Summary: "via anthropic", Positive: "p"}}
			geminiFake := &fakeGemini{review: Review{Summary: "via gemini", Positive: "p"}}
			anthropicSrv := httptest.NewServer(anthropicFake.handler(t))
			defer anthropicSrv.Close()
			geminiSrv := httptest.NewServer(geminiFake.handler(t))
			defer geminiSrv.Close()

			s := newServer(c.envAnthropic, c.envGemini, "test-model", "test-gemini-model", 5*time.Second)
			s.baseURL = anthropicSrv.URL
			s.geminiURL = geminiSrv.URL

			req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(`{"code":"package a"}`))
			if c.headerAnthropic != "" {
				req.Header.Set("X-API-Key", c.headerAnthropic)
			}
			if c.headerGemini != "" {
				req.Header.Set("X-Gemini-Key", c.headerGemini)
			}
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
			}
			var resp reviewResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if resp.Meta.Provider != c.wantProvider || resp.Meta.Model != c.wantModel {
				t.Fatalf("provider = %q model = %q, want %q %q", resp.Meta.Provider, resp.Meta.Model, c.wantProvider, c.wantModel)
			}
			geminiCalls, geminiKey, _ := geminiFake.stats()
			anthropicCalls, _, _ := anthropicFake.stats()
			if c.wantProvider == "gemini" {
				if geminiCalls != 1 || anthropicCalls != 0 {
					t.Fatalf("gemini calls = %d, anthropic calls = %d", geminiCalls, anthropicCalls)
				}
				wantKey := c.headerGemini
				if wantKey == "" {
					wantKey = c.envGemini
				}
				if geminiKey != wantKey {
					t.Fatalf("gemini saw key %q, want %q", geminiKey, wantKey)
				}
			} else if anthropicCalls != 1 || geminiCalls != 0 {
				t.Fatalf("anthropic calls = %d, gemini calls = %d", anthropicCalls, geminiCalls)
			}
		})
	}
}

func TestIndexPageHasAPIKeyField(t *testing.T) {
	handler, done := newTestServer(t, &fakeAPI{})
	defer done()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	wants := []string{
		`id="apikey"`, `type="password"`, "X-API-Key",
		`id="geminikey"`, "X-Gemini-Key", "keyprint",
		`id="prurl"`, `id="ghtoken"`, "X-GitHub-Token",
		`id="usage"`, `id="grade"`, `id="tab-pr"`, "big_pr",
		"data-tip", "renderSnippet", "highlight_index",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}

const sampleDiff = "diff --git a/pkg/x.go b/pkg/x.go\n--- a/pkg/x.go\n+++ b/pkg/x.go\n@@ -1,2 +1,3 @@\n-old\n+new\n+another\n context\n"

func newPRTestServer(t *testing.T, fake *fakeAPI, gh http.HandlerFunc) (*server, func()) {
	anthropic := httptest.NewServer(fake.handler(t))
	github := httptest.NewServer(gh)
	s := newServer("server-key", "", "test-model", "test-gemini-model", 5*time.Second)
	s.baseURL = anthropic.URL
	s.githubAPI = github.URL
	return s, func() {
		anthropic.Close()
		github.Close()
	}
}

func postReview(handler http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(body))
	req.Header.Set("X-GitHub-Token", "gh-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		raw     string
		want    prRef
		wantErr bool
	}{
		{"https://github.com/octo/demo/pull/42", prRef{"octo", "demo", 42}, false},
		{"https://github.com/octo/demo/pull/7/files", prRef{"octo", "demo", 7}, false},
		{"github.com/octo/demo/pull/3", prRef{"octo", "demo", 3}, false},
		{"https://www.github.com/a-b/c.d/pull/1", prRef{"a-b", "c.d", 1}, false},
		{"https://gitlab.com/octo/demo/pull/1", prRef{}, true},
		{"https://github.com.evil.com/octo/demo/pull/1", prRef{}, true},
		{"https://github.com/octo/demo/issues/1", prRef{}, true},
		{"https://github.com/octo/demo/pull/abc", prRef{}, true},
		{"ftp://github.com/octo/demo/pull/1", prRef{}, true},
		{"", prRef{}, true},
	}
	for _, c := range cases {
		got, err := parsePRURL(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("parsePRURL(%q) succeeded with %+v, want error", c.raw, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePRURL(%q): %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePRURL(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestCountDiffStats(t *testing.T) {
	files, additions, deletions := countDiffStats(sampleDiff)
	if files != 1 || additions != 2 || deletions != 1 {
		t.Fatalf("countDiffStats = %d files, +%d, -%d; want 1, +2, -1", files, additions, deletions)
	}
}

func TestReviewHandlerPRFlow(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "diff looks fine", Positive: "small change"}}
	gh := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/demo/pulls/42" {
			t.Errorf("GitHub path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.v3.diff" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gh-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Write([]byte(sampleDiff))
	}
	s, done := newPRTestServer(t, fake, gh)
	defer done()

	rec := postReview(s.routes(), `{"pr_url":"https://github.com/octo/demo/pull/42"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	var resp reviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Summary != "diff looks fine" {
		t.Errorf("summary = %q", resp.Summary)
	}
	meta := resp.Meta
	if meta.Source != "pr" || meta.Files != 1 || meta.Additions != 2 || meta.Deletions != 1 || meta.Language != "go" {
		t.Errorf("unexpected meta: %+v", meta)
	}
	wantUsage := Usage{InputTokens: fakeInputTokens, OutputTokens: fakeOutputTokens, Requests: 1}
	if meta.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", meta.Usage, wantUsage)
	}
}

func TestReviewHandlerPRTooBig(t *testing.T) {
	gh := func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("diff --git a/big b/big\n"))
		w.Write([]byte(strings.Repeat("+", maxPRDiffBytes)))
	}
	s, done := newPRTestServer(t, &fakeAPI{}, gh)
	defer done()

	rec := postReview(s.routes(), `{"pr_url":"https://github.com/octo/demo/pull/42"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		BigPR      bool `json:"big_pr"`
		LimitBytes int  `json:"limit_bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.BigPR || resp.LimitBytes != maxPRDiffBytes {
		t.Fatalf("unexpected big-PR payload: %+v", resp)
	}
}

func TestReviewHandlerPRErrors(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		ghStatus   int
		wantStatus int
	}{
		{"bad host", `{"pr_url":"https://gitlab.com/o/r/pull/1"}`, http.StatusOK, http.StatusBadRequest},
		{"not a pr path", `{"pr_url":"https://github.com/o/r/issues/1"}`, http.StatusOK, http.StatusBadRequest},
		{"pr not found", `{"pr_url":"https://github.com/o/r/pull/1"}`, http.StatusNotFound, http.StatusBadGateway},
		{"rate limited", `{"pr_url":"https://github.com/o/r/pull/1"}`, http.StatusForbidden, http.StatusBadGateway},
		{"neither code nor pr", `{"code":"  "}`, http.StatusOK, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.ghStatus)
				w.Write([]byte("x"))
			}
			s, done := newPRTestServer(t, &fakeAPI{}, gh)
			defer done()
			rec := postReview(s.routes(), c.body)
			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestResolveAddr(t *testing.T) {
	cases := []struct {
		name     string
		flagAddr string
		envPort  string
		want     string
	}{
		{"no env keeps local default", defaultServeAddr, "", defaultServeAddr},
		{"env port binds all interfaces", defaultServeAddr, "8080", "0.0.0.0:8080"},
		{"env port with colon prefix", defaultServeAddr, ":7860", "0.0.0.0:7860"},
		{"explicit addr wins over env", "127.0.0.1:9999", "8080", "127.0.0.1:9999"},
		{"explicit addr without env", "0.0.0.0:1234", "", "0.0.0.0:1234"},
	}
	for _, c := range cases {
		if got := resolveAddr(c.flagAddr, c.envPort); got != c.want {
			t.Errorf("%s: resolveAddr(%q, %q) = %q, want %q", c.name, c.flagAddr, c.envPort, got, c.want)
		}
	}
}

func TestClientIPProxyHandling(t *testing.T) {
	cases := []struct {
		name       string
		trustProxy bool
		forwarded  string
		remoteAddr string
		want       string
	}{
		{"direct connection", false, "", "10.0.0.9:4321", "10.0.0.9"},
		{"spoofed header ignored when untrusted", false, "6.6.6.6", "10.0.0.9:4321", "10.0.0.9"},
		{"trusted proxy uses rightmost hop", true, "6.6.6.6, 203.0.113.7", "127.0.0.1:4321", "203.0.113.7"},
		{"trusted proxy single hop", true, "203.0.113.7", "127.0.0.1:4321", "203.0.113.7"},
		{"trusted proxy skips invalid entries", true, "garbage, also-bad", "127.0.0.1:4321", "127.0.0.1"},
		{"trusted proxy without header falls back", true, "", "127.0.0.1:4321", "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newServer("k", "", "m", "gm", time.Second)
			s.trustProxy = c.trustProxy
			req := httptest.NewRequest(http.MethodPost, "/review", nil)
			req.RemoteAddr = c.remoteAddr
			if c.forwarded != "" {
				req.Header.Set("X-Forwarded-For", c.forwarded)
			}
			if got := s.clientIP(req); got != c.want {
				t.Fatalf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRateLimitSeparatesForwardedClients(t *testing.T) {
	fake := &fakeAPI{review: Review{Summary: "s", Positive: "p"}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	s := newServer("server-key", "", "test-model", "test-gemini-model", 5*time.Second)
	s.baseURL = srv.URL
	s.trustProxy = true
	handler := s.routes()

	send := func(forwardedFor string) int {
		req := httptest.NewRequest(http.MethodPost, "/review", strings.NewReader(`{"code":"package a"}`))
		req.RemoteAddr = "127.0.0.1:9999"
		req.Header.Set("X-Forwarded-For", forwardedFor)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < rateLimitPerMinute; i++ {
		if code := send("203.0.113.7"); code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i+1, code)
		}
	}
	if code := send("203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("exhausted client status = %d, want 429", code)
	}
	if code := send("198.51.100.4"); code != http.StatusOK {
		t.Fatalf("other forwarded client status = %d, want 200", code)
	}
}

func TestRateLimiterCleanupRemovesStaleEntries(t *testing.T) {
	l := newRateLimiter(2, time.Minute)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if !l.allow("1.2.3.4", base) {
		t.Fatal("first request should be allowed")
	}
	l.removeStale(base.Add(2 * time.Minute))
	l.mu.Lock()
	remaining := len(l.hits)
	l.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected stale entries removed, %d keys remain", remaining)
	}
	if !l.allow("1.2.3.4", base.Add(3*time.Minute)) {
		t.Fatal("request after cleanup should be allowed")
	}
}
