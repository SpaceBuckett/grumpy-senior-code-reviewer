package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

//go:embed rubrics/smells.json rubrics/refactorings.json rubrics/go.json
var rubricFS embed.FS

const maxCueSummaryLen = 100

// RubricEntry is one catalog item: a code smell or a piece of Go guidance.
type RubricEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Family       string   `json:"family,omitempty"`
	Topic        string   `json:"topic,omitempty"`
	Description  string   `json:"description"`
	Cues         []string `json:"cues"`
	Refactorings []string `json:"refactorings"`
	Reference    string   `json:"reference"`
}

// RefactoringEntry is a named refactoring technique and the smells it fixes.
type RefactoringEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fixes       []string `json:"fixes"`
	Reference   string   `json:"reference"`
}

// Rubrics holds the catalogs loaded for a review batch.
type Rubrics struct {
	Smells       []RubricEntry
	Refactorings []RefactoringEntry
	GoGuidance   []RubricEntry
}

// LoadRubrics loads the smell and refactoring catalogs, plus Go guidance when lang is "go".
func LoadRubrics(lang string) (Rubrics, error) {
	var r Rubrics
	if err := loadCatalog("rubrics/smells.json", &r.Smells); err != nil {
		return Rubrics{}, err
	}
	if err := loadCatalog("rubrics/refactorings.json", &r.Refactorings); err != nil {
		return Rubrics{}, err
	}
	if lang != "go" {
		return r, nil
	}
	if err := loadCatalog("rubrics/go.json", &r.GoGuidance); err != nil {
		return Rubrics{}, err
	}
	return r, nil
}

func loadCatalog(name string, v any) error {
	data, err := rubricFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("reading embedded catalog %s: %w", name, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parsing catalog %s: %w", name, err)
	}
	return nil
}

// PromptSection renders the loaded catalogs as a compact plain-text block for a system prompt.
func (r Rubrics) PromptSection() string {
	var b strings.Builder
	b.WriteString("CODE SMELL CATALOG (name [family] | cues | fixes):\n")
	for _, s := range r.Smells {
		writeEntryLine(&b, s.Name, s.Family, s.Cues, s.Refactorings)
	}
	b.WriteString("\nREFACTORING CATALOG (name | what it does | smells it fixes):\n")
	for _, t := range r.Refactorings {
		fmt.Fprintf(&b, "- %s | %s | fixes: %s\n", t.Name, t.Description, joinOrNone(t.Fixes))
	}
	if len(r.GoGuidance) == 0 {
		return b.String()
	}
	b.WriteString("\nGO GUIDANCE CATALOG (name [topic] | cues | fixes):\n")
	for _, g := range r.GoGuidance {
		writeEntryLine(&b, g.Name, g.Topic, g.Cues, g.Refactorings)
	}
	return b.String()
}

func writeEntryLine(b *strings.Builder, name, group string, cues, refactorings []string) {
	fmt.Fprintf(b, "- %s [%s] cues: %s | fix: %s\n", name, group, summarizeCues(cues), joinOrNone(refactorings))
}

func summarizeCues(cues []string) string {
	var summary string
	for _, cue := range cues {
		next := summary
		if next != "" {
			next += "; "
		}
		next += cue
		if len(next) > maxCueSummaryLen {
			break
		}
		summary = next
	}
	return summary
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// DetectLanguage maps a file path to the language identifier used to pick rubrics.
func DetectLanguage(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return "go"
	}
	return "generic"
}

func batchLanguage(sources []Source) string {
	for _, s := range sources {
		if sourceLanguage(s) == "go" {
			return "go"
		}
	}
	return "generic"
}

func sourceLanguage(s Source) string {
	if !s.IsDiff {
		return DetectLanguage(s.Name)
	}
	for _, p := range diffPaths(s.Content) {
		if DetectLanguage(p) == "go" {
			return "go"
		}
	}
	return "generic"
}

func diffPaths(diff string) []string {
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		p, ok := strings.CutPrefix(line, "+++ ")
		if !ok {
			p, ok = strings.CutPrefix(line, "--- ")
		}
		if !ok {
			continue
		}
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "a/")
		p = strings.TrimPrefix(p, "b/")
		if p == "" || p == "/dev/null" {
			continue
		}
		paths = append(paths, p)
	}
	return paths
}
