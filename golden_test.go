package main

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestGoldenFixturesAreValidGo(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "golden", "*.go"))
	if err != nil {
		t.Fatalf("globbing fixtures: %v", err)
	}
	if len(files) < 5 {
		t.Fatalf("expected at least 5 golden fixtures, found %d", len(files))
	}
	for _, file := range files {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Errorf("%s does not parse: %v", file, err)
			continue
		}
		if f.Name.Name != "golden" {
			t.Errorf("%s declares package %q, want \"golden\"", file, f.Name.Name)
		}
	}
}
