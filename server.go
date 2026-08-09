package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

const (
	maxReviewBodyBytes = 200 * 1024
	maxPRDiffBytes     = 800 * 1024
	rateLimitPerMinute = 10
	shutdownGrace      = 5 * time.Second
	defaultGitHubAPI   = "https://api.github.com"
	defaultServeAddr   = "127.0.0.1:8787"
)

type reviewRequest struct {
	Code     string `json:"code"`
	PRURL    string `json:"pr_url"`
	Language string `json:"language"`
	Rewrite  bool   `json:"rewrite"`
}

type reviewResponse struct {
	*Review
	Meta reviewMeta `json:"meta"`
}

type reviewMeta struct {
	Source    string `json:"source"`
	PRURL     string `json:"pr_url,omitempty"`
	Files     int    `json:"files,omitempty"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
	DiffBytes int    `json:"diff_bytes,omitempty"`
	Language  string `json:"language"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Usage     Usage  `json:"usage"`
}

type server struct {
	anthropicKey string
	geminiKey    string
	model        string
	geminiModel  string
	baseURL      string
	geminiURL    string
	githubAPI    string
	trustProxy   bool
	timeout      time.Duration
	limiter      *rateLimiter
	http         *http.Client
}

func newServer(anthropicKey, geminiKey, model, geminiModel string, timeout time.Duration) *server {
	return &server{
		anthropicKey: anthropicKey,
		geminiKey:    geminiKey,
		model:        model,
		geminiModel:  geminiModel,
		githubAPI:    defaultGitHubAPI,
		timeout:      timeout,
		limiter:      newRateLimiter(rateLimitPerMinute, time.Minute),
		http:         &http.Client{},
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", defaultServeAddr, "address to listen on; the PORT environment variable overrides the default")
	model := fs.String("model", "claude-sonnet-4-6", "Claude model to use")
	geminiModel := fs.String("gemini-model", "gemini-2.5-flash", "Gemini model to use")
	timeout := fs.Duration("timeout", 120*time.Second, "per-review deadline")
	trustProxy := fs.Bool("trust-proxy", false, "trust X-Forwarded-For from a reverse proxy for rate limiting")
	if err := fs.Parse(args); err != nil {
		return err
	}

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	if anthropicKey == "" && geminiKey == "" {
		log.Print("no ANTHROPIC_API_KEY or GEMINI_API_KEY set; every request must supply its own key")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s := newServer(anthropicKey, geminiKey, *model, *geminiModel, *timeout)
	s.trustProxy = *trustProxy
	go s.limiter.cleanupLoop(ctx)

	listenAddr := resolveAddr(*addr, os.Getenv("PORT"))
	httpServer := &http.Server{Addr: listenAddr, Handler: s.routes()}
	log.Printf("grumpysenior serving on %s", serveURL(listenAddr))

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

func resolveAddr(flagAddr, envPort string) string {
	if envPort == "" || flagAddr != defaultServeAddr {
		return flagAddr
	}
	return net.JoinHostPort("0.0.0.0", strings.TrimPrefix(envPort, ":"))
}

func serveURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /review", s.handleReview)
	return mux
}

func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(indexHTML)
}

func (s *server) handleReview(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow(s.clientIP(r), time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, fmt.Sprintf("rate limit exceeded: %d reviews per minute", rateLimitPerMinute))
		return
	}

	provider, apiKey := s.pickProvider(r)
	if apiKey == "" {
		writeJSONError(w, http.StatusUnauthorized, "provide an Anthropic key (X-API-Key header) or a Gemini key (X-Gemini-Key header)")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxReviewBodyBytes)
	var req reviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %dKB", maxReviewBodyBytes/1024))
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()

	sources, meta, err := s.buildSources(ctx, req, strings.TrimSpace(r.Header.Get("X-GitHub-Token")))
	if err != nil {
		var tooBig *prTooBigError
		switch {
		case errors.As(err, &tooBig):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":       tooBig.Error(),
				"big_pr":      true,
				"diff_bytes":  tooBig.size,
				"limit_bytes": maxPRDiffBytes,
			})
		case errors.Is(err, errBadReviewInput):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		default:
			writeJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	meta.Provider = provider
	meta.Model = s.modelFor(provider)
	review, usage, err := s.runReview(ctx, provider, apiKey, sources, meta.Language, req.Rewrite)
	meta.Usage = usage
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "review failed: "+err.Error())
		return
	}
	attachSnippets(review, sources)
	writeJSON(w, http.StatusOK, reviewResponse{Review: review, Meta: meta})
}

func (s *server) pickProvider(r *http.Request) (provider, apiKey string) {
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return providerAnthropic, key
	}
	if key := strings.TrimSpace(r.Header.Get("X-Gemini-Key")); key != "" {
		return providerGemini, key
	}
	if s.anthropicKey != "" {
		return providerAnthropic, s.anthropicKey
	}
	if s.geminiKey != "" {
		return providerGemini, s.geminiKey
	}
	return "", ""
}

func (s *server) modelFor(provider string) string {
	if provider == providerGemini {
		return s.geminiModel
	}
	return s.model
}

var errBadReviewInput = errors.New("bad review input")

func (s *server) buildSources(ctx context.Context, req reviewRequest, ghToken string) ([]Source, reviewMeta, error) {
	if strings.TrimSpace(req.PRURL) != "" {
		return s.buildPRSource(ctx, req.PRURL, ghToken)
	}
	if strings.TrimSpace(req.Code) == "" {
		return nil, reviewMeta{}, fmt.Errorf("%w: provide code or a pull request URL", errBadReviewInput)
	}
	lang, name := "generic", "input.txt"
	if req.Language == "go" {
		lang, name = "go", "input.go"
	}
	meta := reviewMeta{Source: "code", Language: lang}
	return []Source{{Name: name, Content: req.Code}}, meta, nil
}

func (s *server) buildPRSource(ctx context.Context, prURL, ghToken string) ([]Source, reviewMeta, error) {
	ref, err := parsePRURL(prURL)
	if err != nil {
		return nil, reviewMeta{}, fmt.Errorf("%w: %s", errBadReviewInput, err)
	}
	diff, err := s.fetchPRDiff(ctx, ref, ghToken)
	if err != nil {
		return nil, reviewMeta{}, err
	}
	sources := []Source{{Name: "diff", Content: diff, IsDiff: true}}
	files, additions, deletions := countDiffStats(diff)
	meta := reviewMeta{
		Source:    "pr",
		PRURL:     ref.String(),
		Files:     files,
		Additions: additions,
		Deletions: deletions,
		DiffBytes: len(diff),
		Language:  batchLanguage(sources),
	}
	return sources, meta, nil
}

func (s *server) runReview(ctx context.Context, provider, apiKey string, sources []Source, lang string, rewrite bool) (*Review, Usage, error) {
	rubrics, err := LoadRubrics(lang)
	if err != nil {
		return nil, Usage{}, err
	}
	client := NewClient(apiKey, s.model, rubrics)
	override := s.baseURL
	if provider == providerGemini {
		client = NewGeminiClient(apiKey, s.geminiModel, rubrics)
		override = s.geminiURL
	}
	if override != "" {
		client.baseURL = override
	}
	return client.Review(ctx, sources, rewrite)
}

func (s *server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if ip := forwardedClientIP(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func forwardedClientIP(header string) string {
	hops := strings.Split(header, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(hops[i])
		if net.ParseIP(candidate) != nil {
			return candidate
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: make(map[string][]time.Time)}
}

func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := pruneOlder(l.hits[key], now.Add(-l.window))
	if len(kept) >= l.limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func (l *rateLimiter) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(l.window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			l.removeStale(now)
		}
	}
}

func (l *rateLimiter) removeStale(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	for key, times := range l.hits {
		kept := pruneOlder(times, cutoff)
		if len(kept) == 0 {
			delete(l.hits, key)
			continue
		}
		l.hits[key] = kept
	}
}

func pruneOlder(times []time.Time, cutoff time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}
