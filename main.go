package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxFileBytes = 100 * 1024
	version      = "1.0.0"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := runServe(os.Args[2:]); err != nil {
			fatal(err.Error())
		}
		return
	}

	var (
		diffMode    = flag.Bool("diff", false, "read a unified diff from stdin instead of files")
		format      = flag.String("format", "pretty", "output format: pretty | json | markdown")
		model       = flag.String("model", "claude-sonnet-4-6", "Claude model to use")
		failOn      = flag.String("fail-on", "high", "minimum severity that causes exit code 1: high | medium | low | never")
		rewrite     = flag.Bool("rewrite", false, "request suggested rewrites for high and medium findings")
		timeout     = flag.Duration("timeout", 120*time.Second, "overall deadline for the review")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("grumpysenior", version)
		return
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fatal("ANTHROPIC_API_KEY environment variable is not set")
	}

	sources, err := collectSources(*diffMode, flag.Args())
	if err != nil {
		fatal(err.Error())
	}
	if len(sources) == 0 {
		fatal("nothing to review: pass file paths, or pipe a diff with --diff")
	}

	rubrics, err := LoadRubrics(batchLanguage(sources))
	if err != nil {
		fatal(err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client := NewClient(apiKey, *model, rubrics)
	review, _, err := client.Review(ctx, sources, *rewrite)
	if err != nil {
		fatal("review failed: " + err.Error())
	}

	switch *format {
	case "json":
		if err := review.WriteJSON(os.Stdout); err != nil {
			fatal(err.Error())
		}
	case "markdown":
		review.WriteMarkdown(os.Stdout)
	default:
		review.WritePretty(os.Stdout)
	}

	if review.ShouldFail(*failOn) {
		os.Exit(1)
	}
}

// Source is a named piece of code to review, either a file or a diff.
type Source struct {
	Name    string
	Content string
	IsDiff  bool
}

func collectSources(diffMode bool, paths []string) ([]Source, error) {
	if diffMode {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, maxFileBytes*4))
		if err != nil {
			return nil, fmt.Errorf("reading diff from stdin: %w", err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			return nil, nil
		}
		return []Source{{Name: "diff", Content: text, IsDiff: true}}, nil
	}

	var sources []Source
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory; pass files or use --diff", p)
		}
		if info.Size() > maxFileBytes {
			return nil, fmt.Errorf("%s exceeds %dKB; review it in smaller pieces", p, maxFileBytes/1024)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		sources = append(sources, Source{Name: filepath.ToSlash(p), Content: string(data)})
	}
	return sources, nil
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "grumpysenior:", msg)
	os.Exit(2)
}
