package main

import (
	"regexp"
	"strconv"
	"strings"
)

const snippetContext = 3

func attachSnippets(review *Review, sources []Source) {
	for i := range review.Findings {
		review.Findings[i].Snippet = findingSnippet(review.Findings[i], sources)
	}
}

func findingSnippet(f Finding, sources []Source) *Snippet {
	src := sourceForFinding(f, sources)
	if src == nil {
		return nil
	}
	if src.IsDiff {
		return diffSnippet(f, src.Content)
	}
	return codeSnippet(f, src)
}

func codeSnippet(f Finding, src *Source) *Snippet {
	lines := strings.Split(src.Content, "\n")
	target := lineNumberOf(f.Line)
	if target == 0 {
		target = symbolLineOf(lines, f.Line)
	}
	if target == 0 || target > len(lines) {
		return nil
	}
	start := max(1, target-snippetContext)
	end := min(len(lines), target+snippetContext)
	return &Snippet{
		File:           src.Name,
		StartLine:      start,
		HighlightIndex: target - start,
		Lines:          lines[start-1 : end],
	}
}

func lineNumberOf(line string) int {
	line = strings.TrimSpace(line)
	digits := line
	for i, r := range line {
		if r < '0' || r > '9' {
			digits = line[:i]
			break
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func symbolLineOf(lines []string, symbol string) int {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return 0
	}
	for i, line := range lines {
		if strings.Contains(line, symbol) {
			return i + 1
		}
	}
	return 0
}

type diffEntry struct {
	text    string
	file    string
	newLine int
}

var hunkNewStart = regexp.MustCompile(`\+([0-9]+)`)

func parseDiffEntries(diff string) []diffEntry {
	var entries []diffEntry
	var file string
	var newLine int
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")), "b/")
			inHunk = false
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			newLine = hunkNewLineStart(line)
			inHunk = newLine > 0
		case !inHunk, strings.HasPrefix(line, `\`):
		case strings.HasPrefix(line, "-"):
			entries = append(entries, diffEntry{text: line, file: file})
		default:
			entries = append(entries, diffEntry{text: line, file: file, newLine: newLine})
			newLine++
		}
	}
	return entries
}

func hunkNewLineStart(header string) int {
	m := hunkNewStart.FindStringSubmatch(header)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func diffSnippet(f Finding, diff string) *Snippet {
	entries := parseDiffEntries(diff)
	idx := -1
	if target := lineNumberOf(f.Line); target > 0 {
		for i, e := range entries {
			if diffFileMatches(e.file, f.File) && e.newLine == target {
				idx = i
				break
			}
		}
	}
	if idx < 0 && strings.TrimSpace(f.Line) != "" {
		for i, e := range entries {
			if diffFileMatches(e.file, f.File) && strings.Contains(e.text, strings.TrimSpace(f.Line)) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return nil
	}
	start := max(0, idx-snippetContext)
	end := min(len(entries), idx+snippetContext+1)
	for start < idx && entries[start].file != entries[idx].file {
		start++
	}
	for end > idx+1 && entries[end-1].file != entries[idx].file {
		end--
	}
	lines := make([]string, 0, end-start)
	for _, e := range entries[start:end] {
		lines = append(lines, e.text)
	}
	return &Snippet{
		File:           entries[idx].file,
		StartLine:      entries[start].newLine,
		HighlightIndex: idx - start,
		Lines:          lines,
		Diff:           true,
	}
}

func diffFileMatches(entryFile, findingFile string) bool {
	if findingFile == "diff" || findingFile == "" {
		return true
	}
	return entryFile == findingFile ||
		strings.HasSuffix(entryFile, "/"+findingFile) ||
		strings.HasSuffix(findingFile, "/"+entryFile)
}
