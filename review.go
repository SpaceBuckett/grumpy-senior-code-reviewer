package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Review is the structured verdict returned by the model.
type Review struct {
	Summary  string    `json:"summary"`
	Positive string    `json:"positive"`
	Findings []Finding `json:"findings"`
}

type Finding struct {
	File        string   `json:"file"`
	Line        string   `json:"line"`
	Severity    string   `json:"severity"`
	Category    string   `json:"category"`
	Issue       string   `json:"issue"`
	Suggestion  string   `json:"suggestion"`
	Smell       string   `json:"smell,omitempty"`
	Refactoring string   `json:"refactoring,omitempty"`
	Reference   string   `json:"reference,omitempty"`
	Rewrite     string   `json:"rewrite,omitempty"`
	Snippet     *Snippet `json:"snippet,omitempty"`
}

// Snippet is the code excerpt a finding refers to, attached for display in the web UI.
type Snippet struct {
	File           string   `json:"file"`
	StartLine      int      `json:"start_line,omitempty"`
	HighlightIndex int      `json:"highlight_index"`
	Lines          []string `json:"lines"`
	Diff           bool     `json:"diff,omitempty"`
}

var severityRank = map[string]int{"high": 3, "medium": 2, "low": 1}

func (r *Review) sorted() []Finding {
	out := make([]Finding, len(r.Findings))
	copy(out, r.Findings)
	sort.SliceStable(out, func(i, j int) bool {
		if severityRank[out[i].Severity] != severityRank[out[j].Severity] {
			return severityRank[out[i].Severity] > severityRank[out[j].Severity]
		}
		return out[i].File < out[j].File
	})
	return out
}

// ShouldFail reports whether any finding meets the CI failure threshold.
func (r *Review) ShouldFail(threshold string) bool {
	min, ok := severityRank[threshold]
	if !ok {
		return false
	}
	for _, f := range r.Findings {
		if severityRank[f.Severity] >= min {
			return true
		}
	}
	return false
}

func (r *Review) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r *Review) WritePretty(w io.Writer) {
	const (
		bold  = "\033[1m"
		red   = "\033[31m"
		yel   = "\033[33m"
		cyan  = "\033[36m"
		green = "\033[32m"
		dim   = "\033[2m"
		reset = "\033[0m"
	)
	color := map[string]string{"high": red, "medium": yel, "low": cyan}

	fmt.Fprintf(w, "%s── GrumpySenior Review ──%s\n\n%s\n\n", bold, reset, r.Summary)
	fmt.Fprintf(w, "%s✔ Done well:%s %s\n\n", green, reset, r.Positive)

	if len(r.Findings) == 0 {
		fmt.Fprintf(w, "%sNo findings. Ready for human review.%s\n", green, reset)
		return
	}
	for i, f := range r.sorted() {
		fmt.Fprintf(w, "%d. %s[%s]%s %s(%s)%s %s\n", i+1,
			color[f.Severity], f.Severity, reset, bold, f.Category, reset, f.location())
		fmt.Fprintf(w, "   issue: %s\n   fix:   %s\n", f.Issue, f.Suggestion)
		if f.Smell != "" {
			fmt.Fprintf(w, "   smell: %s\n", f.Smell)
		}
		if f.Refactoring != "" {
			fmt.Fprintf(w, "   refactor: %s\n", f.Refactoring)
		}
		if f.Reference != "" {
			fmt.Fprintf(w, "   %s%s%s\n", dim, f.Reference, reset)
		}
		if f.Rewrite != "" {
			for _, line := range strings.Split(strings.TrimRight(f.Rewrite, "\n"), "\n") {
				fmt.Fprintf(w, "       %s\n", line)
			}
		}
		fmt.Fprintln(w)
	}
}

func (f Finding) location() string {
	if f.Line == "" {
		return f.File
	}
	return f.File + ":" + f.Line
}

func (r *Review) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "## GrumpySenior Review\n\n%s\n\n**Done well:** %s\n\n", r.Summary, r.Positive)
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "_No findings. Ready for human review._")
		return
	}
	fmt.Fprintln(w, "| # | Severity | Category | Location | Smell | Refactoring | Issue | Suggested fix |")
	fmt.Fprintln(w, "|---|----------|----------|----------|-------|-------------|-------|---------------|")
	sorted := r.sorted()
	for i, f := range sorted {
		fmt.Fprintf(w, "| %d | %s | %s | `%s` | %s | %s | %s | %s |\n",
			i+1, f.Severity, f.Category, f.location(), markdownSmellCell(f), f.Refactoring, f.Issue, f.Suggestion)
	}
	for i, f := range sorted {
		if f.Rewrite == "" {
			continue
		}
		fmt.Fprintf(w, "\n<details><summary>Suggested rewrite (finding %d)</summary>\n\n```\n%s\n```\n\n</details>\n",
			i+1, strings.TrimRight(f.Rewrite, "\n"))
	}
}

func markdownSmellCell(f Finding) string {
	if f.Smell == "" {
		return ""
	}
	if f.Reference == "" {
		return f.Smell
	}
	return fmt.Sprintf("[%s](%s)", f.Smell, f.Reference)
}
