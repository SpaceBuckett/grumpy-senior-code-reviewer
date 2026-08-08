package main

import (
	"strings"
	"testing"
)

func numberedSource(n int) Source {
	var lines []string
	for i := 1; i <= n; i++ {
		lines = append(lines, "line "+strings.Repeat("x", i))
	}
	return Source{Name: "input.go", Content: strings.Join(lines, "\n")}
}

func TestCodeSnippetByLineNumber(t *testing.T) {
	src := numberedSource(10)
	s := findingSnippet(Finding{File: "input.go", Line: "5"}, []Source{src})
	if s == nil {
		t.Fatal("expected a snippet")
	}
	if s.StartLine != 2 || len(s.Lines) != 7 || s.HighlightIndex != 3 {
		t.Fatalf("snippet = %+v", s)
	}
	if s.Lines[s.HighlightIndex] != "line xxxxx" {
		t.Fatalf("highlighted line = %q", s.Lines[s.HighlightIndex])
	}
}

func TestCodeSnippetClampsAtFileStart(t *testing.T) {
	s := findingSnippet(Finding{File: "input.go", Line: "1"}, []Source{numberedSource(10)})
	if s == nil {
		t.Fatal("expected a snippet")
	}
	if s.StartLine != 1 || s.HighlightIndex != 0 || len(s.Lines) != 4 {
		t.Fatalf("snippet = %+v", s)
	}
}

func TestCodeSnippetBySymbol(t *testing.T) {
	src := Source{Name: "input.go", Content: "package a\n\nfunc Process() {\n\treturn\n}\n"}
	s := findingSnippet(Finding{File: "input.go", Line: "Process"}, []Source{src})
	if s == nil {
		t.Fatal("expected a snippet")
	}
	if s.Lines[s.HighlightIndex] != "func Process() {" {
		t.Fatalf("highlighted line = %q", s.Lines[s.HighlightIndex])
	}
}

func TestCodeSnippetMissingTarget(t *testing.T) {
	cases := []string{"", "999", "NoSuchSymbol"}
	for _, line := range cases {
		if s := findingSnippet(Finding{File: "input.go", Line: line}, []Source{numberedSource(5)}); s != nil {
			t.Errorf("Line %q: expected no snippet, got %+v", line, s)
		}
	}
}

func TestDiffSnippetByNewLineNumber(t *testing.T) {
	src := Source{Name: "diff", Content: sampleDiff, IsDiff: true}
	s := findingSnippet(Finding{File: "pkg/x.go", Line: "2"}, []Source{src})
	if s == nil {
		t.Fatal("expected a snippet")
	}
	if !s.Diff || s.File != "pkg/x.go" {
		t.Fatalf("snippet = %+v", s)
	}
	if s.Lines[s.HighlightIndex] != "+another" {
		t.Fatalf("highlighted line = %q", s.Lines[s.HighlightIndex])
	}
}

func TestDiffSnippetForWholeDiffFile(t *testing.T) {
	src := Source{Name: "diff", Content: sampleDiff, IsDiff: true}
	s := findingSnippet(Finding{File: "diff", Line: "1"}, []Source{src})
	if s == nil {
		t.Fatal("expected a snippet")
	}
	if s.Lines[s.HighlightIndex] != "+new" {
		t.Fatalf("highlighted line = %q", s.Lines[s.HighlightIndex])
	}
}

func TestDiffSnippetUnknownFile(t *testing.T) {
	src := Source{Name: "diff", Content: sampleDiff, IsDiff: true}
	if s := findingSnippet(Finding{File: "other.go", Line: "2"}, []Source{src}); s != nil {
		t.Fatalf("expected no snippet, got %+v", s)
	}
}
