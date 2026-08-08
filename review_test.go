package main

import (
	"strings"
	"testing"
)

func TestParseReviewToleratesFences(t *testing.T) {
	raw := "```json\n{\"summary\":\"ok\",\"positive\":\"good names\",\"findings\":[]}\n```"
	r, err := parseReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Summary != "ok" || r.Positive != "good names" {
		t.Fatalf("bad parse: %+v", r)
	}
}

func TestParseReviewRejectsGarbage(t *testing.T) {
	if _, err := parseReview("not json at all"); err == nil {
		t.Fatal("expected error for non-JSON input")
	}
}

func TestShouldFailThresholds(t *testing.T) {
	r := &Review{Findings: []Finding{{Severity: "medium"}}}
	cases := []struct {
		threshold string
		want      bool
	}{
		{"high", false},
		{"medium", true},
		{"low", true},
		{"never", false},
	}
	for _, c := range cases {
		if got := r.ShouldFail(c.threshold); got != c.want {
			t.Errorf("ShouldFail(%q) = %v, want %v", c.threshold, got, c.want)
		}
	}
}

func TestParseReviewAcceptsLegacyFindings(t *testing.T) {
	raw := `{"summary":"ok","positive":"fine","findings":[{"file":"a.go","line":"3","severity":"low","category":"readability","issue":"x","suggestion":"y"}]}`
	r, err := parseReview(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f := r.Findings[0]
	if f.Smell != "" || f.Refactoring != "" || f.Reference != "" || f.Rewrite != "" {
		t.Fatalf("expected empty extended fields, got %+v", f)
	}
}

func TestWriteMarkdownRewriteDetails(t *testing.T) {
	withRewrite := Finding{
		File: "a.go", Severity: "high", Category: "structure",
		Issue: "too long", Suggestion: "split it",
		Smell: "Long Method", Refactoring: "Extract Method",
		Reference: "https://refactoring.guru/smells/long-method",
		Rewrite:   "func small() {}",
	}
	cases := []struct {
		name        string
		finding     Finding
		wantDetails bool
	}{
		{"rewrite present", withRewrite, true},
		{"rewrite absent", Finding{File: "a.go", Severity: "low", Issue: "x", Suggestion: "y"}, false},
	}
	for _, c := range cases {
		var b strings.Builder
		r := &Review{Summary: "s", Positive: "p", Findings: []Finding{c.finding}}
		r.WriteMarkdown(&b)
		out := b.String()
		if got := strings.Contains(out, "<details><summary>Suggested rewrite (finding 1)</summary>"); got != c.wantDetails {
			t.Errorf("%s: details block present = %v, want %v\noutput:\n%s", c.name, got, c.wantDetails, out)
		}
		if !strings.Contains(out, "| Smell | Refactoring |") {
			t.Errorf("%s: missing smell/refactoring columns", c.name)
		}
	}
}

func TestWritePrettyExtendedFields(t *testing.T) {
	r := &Review{Summary: "s", Positive: "p", Findings: []Finding{{
		File: "a.go", Line: "10", Severity: "medium", Category: "structure",
		Issue: "dup", Suggestion: "extract",
		Smell: "Duplicate Code", Refactoring: "Extract Method",
		Reference: "https://refactoring.guru/smells/duplicate-code",
		Rewrite:   "func shared() {}",
	}}}
	var b strings.Builder
	r.WritePretty(&b)
	out := b.String()
	for _, want := range []string{"smell: Duplicate Code", "refactor: Extract Method", "refactoring.guru/smells/duplicate-code", "func shared() {}"} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestSortedOrdersBySeverity(t *testing.T) {
	r := &Review{Findings: []Finding{
		{Severity: "low", File: "a.go"},
		{Severity: "high", File: "b.go"},
		{Severity: "medium", File: "c.go"},
	}}
	got := r.sorted()
	if got[0].Severity != "high" || got[1].Severity != "medium" || got[2].Severity != "low" {
		t.Fatalf("wrong order: %+v", got)
	}
}
