package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const (
	defaultAPIURL         = "https://api.anthropic.com/v1/messages"
	defaultGeminiURL      = "https://generativelanguage.googleapis.com"
	providerAnthropic     = "anthropic"
	providerGemini        = "gemini"
	maxRewriteFindings    = 5
	maxRewriteConcurrency = 3
)

// Client calls an LLM provider API (Anthropic or Gemini) using only the standard library.
type Client struct {
	provider string
	apiKey   string
	model    string
	rubrics  Rubrics
	baseURL  string
	http     *http.Client
}

// NewClient builds a Client that reviews code via the Anthropic Messages API.
func NewClient(apiKey, model string, rubrics Rubrics) *Client {
	return &Client{
		provider: providerAnthropic,
		apiKey:   apiKey,
		model:    model,
		rubrics:  rubrics,
		baseURL:  defaultAPIURL,
		http:     &http.Client{},
	}
}

// NewGeminiClient builds a Client that reviews code via the Gemini API.
func NewGeminiClient(apiKey, model string, rubrics Rubrics) *Client {
	return &Client{
		provider: providerGemini,
		apiKey:   apiKey,
		model:    model,
		rubrics:  rubrics,
		baseURL:  defaultGeminiURL,
		http:     &http.Client{},
	}
}

func buildSystemPrompt(r Rubrics) string {
	var b strings.Builder
	b.WriteString(`You are a senior software engineer performing a pre-review pass on code
before it reaches human reviewers. Your job is to catch issues that waste human review time,
so humans can focus on design and domain logic.

Review the code strictly against three axes:
1. READABILITY  — naming, clarity, comments that lie or are missing where needed, dead code.
2. STRUCTURE    — function size, separation of concerns, duplication, error-handling patterns,
                  concurrency hazards, API shape.
3. MAINTAINABILITY — testability, hidden coupling, magic values, fragile assumptions,
                  missing input validation, resource leaks.

Classify every finding against these catalogs:

`)
	b.WriteString(r.PromptSection())
	b.WriteString(`
Catalog rules:
- Every finding must name exactly one smell from the catalog by its name in "smell"
  (use a Go guidance entry name for Go-specific issues).
- When the catalog maps a refactoring to that smell, name it in "refactoring" using the
  canonical catalog name. Leave "refactoring" empty when no technique applies.
- Set "reference" to the catalog URL for the chosen entry.
- A finding that fits no catalog entry is only allowed with "smell" set to "uncatalogued",
  and "issue" must include a one-clause justification for why it is uncatalogued.

Rules:
- Report only findings a competent senior reviewer would actually raise. No style nitpicks
  that a formatter or linter would catch (gofmt, indentation, import order).
- Every finding must reference the file and, where possible, the line or symbol.
- Every finding must include a concrete suggested fix, not just a complaint.
- Assign severity: "high" (bugs, data races, leaks, security), "medium" (design/maintainability
  debt), "low" (clarity improvements).
- Always include exactly one "positive" note: something genuinely done well.
- If the code is clean, say so — do not invent findings.

Respond ONLY with a JSON object, no markdown fences, matching this schema:
{
  "summary": "one or two sentence overall assessment",
  "positive": "one thing done well",
  "findings": [
    {
      "file": "path or 'diff'",
      "line": "line number or symbol name, or empty string",
      "severity": "high|medium|low",
      "category": "readability|structure|maintainability",
      "issue": "what is wrong and why it matters",
      "suggestion": "concrete fix",
      "smell": "catalog entry name, or 'uncatalogued'",
      "refactoring": "canonical refactoring name, or empty string",
      "reference": "catalog URL for the chosen entry, or empty string",
      "rewrite": "empty string unless rewrites were requested"
    }
  ]
}`)
	return b.String()
}

const rewriteSystemPrompt = `You are a refactoring assistant. Apply exactly the one named
refactoring to the code identified by the finding. Respond with only the minimal rewritten
snippet: plain code, no explanation, no markdown fences.`

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system"`
	Messages  []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Usage totals the Anthropic tokens and requests consumed by one review run.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Requests     int64 `json:"requests"`
}

func (u *Usage) add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.Requests += other.Requests
}

// Review sends the sources to Claude, parses the verdict, and optionally adds rewrites.
func (c *Client) Review(ctx context.Context, sources []Source, withRewrites bool) (*Review, Usage, error) {
	text, usage, err := c.requestText(ctx, buildSystemPrompt(c.rubrics), reviewUserMessage(sources))
	if err != nil {
		return nil, usage, fmt.Errorf("requesting review: %w", err)
	}
	review, err := parseReview(text)
	if err != nil {
		return nil, usage, err
	}
	if !withRewrites {
		return review, usage, nil
	}
	rewriteUsage, err := c.addRewrites(ctx, review, sources)
	usage.add(rewriteUsage)
	if err != nil {
		return nil, usage, fmt.Errorf("generating rewrites: %w", err)
	}
	return review, usage, nil
}

func reviewUserMessage(sources []Source) string {
	var b strings.Builder
	for _, s := range sources {
		if s.IsDiff {
			b.WriteString("Review this unified diff. Focus on the changed lines but use context.\n\n")
			b.WriteString("```diff\n" + s.Content + "\n```\n")
			continue
		}
		fmt.Fprintf(&b, "FILE: %s\n```\n%s\n```\n\n", s.Name, s.Content)
	}
	return b.String()
}

func (c *Client) addRewrites(ctx context.Context, review *Review, sources []Source) (Usage, error) {
	candidates := rewriteCandidates(review.Findings)
	if len(candidates) == 0 {
		return Usage{}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg      sync.WaitGroup
		usageMu sync.Mutex
		total   Usage
	)
	errs := make(chan error, len(candidates))
	sem := make(chan struct{}, maxRewriteConcurrency)
	for _, idx := range candidates {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			finding := review.Findings[idx]
			text, usage, err := c.requestText(ctx, rewriteSystemPrompt, rewriteUserMessage(finding, sources))
			usageMu.Lock()
			total.add(usage)
			usageMu.Unlock()
			if err != nil {
				errs <- fmt.Errorf("rewrite for %s: %w", finding.location(), err)
				cancel()
				return
			}
			review.Findings[idx].Rewrite = stripCodeFences(text)
		}(idx)
	}
	wg.Wait()
	close(errs)
	return total, <-errs
}

func rewriteCandidates(findings []Finding) []int {
	var idxs []int
	for i, f := range findings {
		if f.Severity == "high" || f.Severity == "medium" {
			idxs = append(idxs, i)
		}
	}
	sort.SliceStable(idxs, func(a, b int) bool {
		return severityRank[findings[idxs[a]].Severity] > severityRank[findings[idxs[b]].Severity]
	})
	if len(idxs) > maxRewriteFindings {
		idxs = idxs[:maxRewriteFindings]
	}
	return idxs
}

func rewriteUserMessage(f Finding, sources []Source) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Apply the refactoring %q to fix the smell %q at %s.\n", f.Refactoring, f.Smell, f.location())
	fmt.Fprintf(&b, "Finding: %s\nSuggested fix: %s\n\n", f.Issue, f.Suggestion)
	source := sourceForFinding(f, sources)
	if source != nil {
		fmt.Fprintf(&b, "SOURCE (%s):\n%s\n\n", source.Name, source.Content)
	}
	b.WriteString("Return only the minimal rewritten snippet that applies the named refactoring.")
	return b.String()
}

func sourceForFinding(f Finding, sources []Source) *Source {
	for i := range sources {
		if sources[i].Name == f.File {
			return &sources[i]
		}
	}
	if len(sources) == 0 {
		return nil
	}
	return &sources[0]
}

func stripCodeFences(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text
	}
	lines = lines[1:]
	if strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func (c *Client) requestText(ctx context.Context, system, user string) (string, Usage, error) {
	if c.provider == providerGemini {
		return c.requestGemini(ctx, system, user)
	}
	return c.requestAnthropic(ctx, system, user)
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	SystemInstruction geminiContent   `json:"system_instruction"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (c *Client) requestGemini(ctx context.Context, system, user string) (string, Usage, error) {
	reqBody, err := json.Marshal(geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: system}}},
		Contents:          []geminiContent{{Role: "user", Parts: []geminiPart{{Text: user}}}},
		GenerationConfig:  geminiGenConfig{MaxOutputTokens: 4096},
	})
	if err != nil {
		return "", Usage{}, fmt.Errorf("encoding request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent", c.baseURL, c.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", Usage{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	var out geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{Requests: 1}, fmt.Errorf("decoding Gemini response: %w", err)
	}
	usage := Usage{
		InputTokens:  out.UsageMetadata.PromptTokenCount,
		OutputTokens: out.UsageMetadata.CandidatesTokenCount,
		Requests:     1,
	}
	if out.Error != nil {
		return "", usage, fmt.Errorf("Gemini API error (%s): %s", out.Error.Status, out.Error.Message)
	}
	if len(out.Candidates) == 0 {
		return "", usage, errors.New("Gemini returned no candidates")
	}

	var text strings.Builder
	for _, part := range out.Candidates[0].Content.Parts {
		text.WriteString(part.Text)
	}
	return text.String(), usage, nil
}

func (c *Client) requestAnthropic(ctx context.Context, system, user string) (string, Usage, error) {
	reqBody, err := json.Marshal(apiRequest{
		Model:     c.model,
		MaxTokens: 4096,
		System:    system,
		Messages:  []apiMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", Usage{}, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", Usage{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	var out apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{Requests: 1}, fmt.Errorf("decoding API response: %w", err)
	}
	usage := Usage{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, Requests: 1}
	if out.Error != nil {
		return "", usage, fmt.Errorf("API error (%s): %s", out.Error.Type, out.Error.Message)
	}

	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String(), usage, nil
}

func parseReview(text string) (*Review, error) {
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i > 0 {
		text = text[i:]
	}
	if i := strings.LastIndex(text, "}"); i >= 0 {
		text = text[:i+1]
	}
	var r Review
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, fmt.Errorf("model returned unparseable review: %w", err)
	}
	return &r, nil
}
