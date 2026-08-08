//go:build eval

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoldenFilesTriggerExpectedSmells(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live evaluation")
	}
	rubrics, err := LoadRubrics("go")
	if err != nil {
		t.Fatalf("LoadRubrics: %v", err)
	}
	client := NewClient(apiKey, "claude-sonnet-4-6", rubrics)

	cases := []struct {
		file    string
		smellID string
	}{
		{"long_method.go", "long-method"},
		{"primitive_obsession.go", "primitive-obsession"},
		{"duplicate_code.go", "duplicate-code"},
		{"sql_injection.go", "sql-parameterization"},
		{"goroutine_leak.go", "goroutine-leaks"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "golden", c.file))
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			review, _, err := client.Review(ctx, []Source{{Name: c.file, Content: string(data)}}, false)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			var found []string
			for _, f := range review.Findings {
				id := smellID(f.Smell)
				if id == c.smellID {
					return
				}
				found = append(found, id)
			}
			t.Errorf("expected smell %q among findings, got %v", c.smellID, found)
		})
	}
}

func smellID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
