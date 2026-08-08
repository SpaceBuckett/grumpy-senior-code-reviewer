package main

import (
	"strings"
	"testing"
)

func TestLoadRubricsCatalogsAreValid(t *testing.T) {
	r, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	if len(r.Smells) < 18 {
		t.Errorf("expected at least 18 smells, got %d", len(r.Smells))
	}
	if len(r.Refactorings) < 14 {
		t.Errorf("expected at least 14 refactorings, got %d", len(r.Refactorings))
	}
	if len(r.GoGuidance) < 15 {
		t.Errorf("expected at least 15 Go entries, got %d", len(r.GoGuidance))
	}
	for _, s := range append(append([]RubricEntry{}, r.Smells...), r.GoGuidance...) {
		if s.ID == "" || s.Name == "" || s.Description == "" || s.Reference == "" || len(s.Cues) == 0 {
			t.Errorf("incomplete entry: %+v", s)
		}
	}
	for _, f := range r.Refactorings {
		if f.ID == "" || f.Name == "" || f.Description == "" || f.Reference == "" {
			t.Errorf("incomplete refactoring: %+v", f)
		}
	}
}

func TestSmellRefactoringReferencesExist(t *testing.T) {
	r, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	known := make(map[string]bool, len(r.Refactorings))
	for _, f := range r.Refactorings {
		known[f.ID] = true
	}
	for _, s := range append(append([]RubricEntry{}, r.Smells...), r.GoGuidance...) {
		for _, id := range s.Refactorings {
			if !known[id] {
				t.Errorf("smell %s references unknown refactoring %q", s.ID, id)
			}
		}
	}
	for _, f := range r.Refactorings {
		smells := make(map[string]bool, len(r.Smells))
		for _, s := range r.Smells {
			smells[s.ID] = true
		}
		for _, id := range f.Fixes {
			if !smells[id] {
				t.Errorf("refactoring %s claims to fix unknown smell %q", f.ID, id)
			}
		}
	}
}

func TestLoadRubricsGoEntriesOnlyForGo(t *testing.T) {
	cases := []struct {
		lang       string
		wantGoDocs bool
	}{
		{"go", true},
		{"generic", false},
		{"python", false},
	}
	for _, c := range cases {
		r, err := LoadRubrics(c.lang)
		if err != nil {
			t.Fatalf("LoadRubrics(%q): %v", c.lang, err)
		}
		if got := len(r.GoGuidance) > 0; got != c.wantGoDocs {
			t.Errorf("LoadRubrics(%q) go guidance present = %v, want %v", c.lang, got, c.wantGoDocs)
		}
	}
}

func TestPromptSectionIsCompact(t *testing.T) {
	r, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	section := r.PromptSection()
	if section == "" {
		t.Fatal("PromptSection returned empty output")
	}
	for _, want := range []string{"Long Method", "Extract Method", "Goroutine Leaks"} {
		if !strings.Contains(section, want) {
			t.Errorf("PromptSection missing %q", want)
		}
	}
	for i, line := range strings.Split(section, "\n") {
		if len(line) > 200 {
			t.Errorf("line %d exceeds 200 characters (%d): %s", i+1, len(line), line)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"pkg/server/handler.GO", "go"},
		{"script.py", "generic"},
		{"README.md", "generic"},
		{"Makefile", "generic"},
	}
	for _, c := range cases {
		if got := DetectLanguage(c.path); got != c.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestBatchLanguage(t *testing.T) {
	goDiff := "diff --git a/x/main.go b/x/main.go\n--- a/x/main.go\n+++ b/x/main.go\n@@ -1 +1 @@\n-old\n+new\n"
	pyDiff := "diff --git a/app.py b/app.py\n--- a/app.py\n+++ b/app.py\n@@ -1 +1 @@\n-old\n+new\n"
	cases := []struct {
		name    string
		sources []Source
		want    string
	}{
		{"single go file", []Source{{Name: "a.go"}}, "go"},
		{"single generic file", []Source{{Name: "a.py"}}, "generic"},
		{"mixed batch unions to go", []Source{{Name: "a.py"}, {Name: "b.go"}}, "go"},
		{"go diff", []Source{{Name: "diff", Content: goDiff, IsDiff: true}}, "go"},
		{"generic diff", []Source{{Name: "diff", Content: pyDiff, IsDiff: true}}, "generic"},
		{"empty", nil, "generic"},
	}
	for _, c := range cases {
		if got := batchLanguage(c.sources); got != c.want {
			t.Errorf("%s: batchLanguage = %q, want %q", c.name, got, c.want)
		}
	}
}
