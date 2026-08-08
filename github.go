package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var prPathPattern = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([0-9]+)(?:/.*)?$`)

type prRef struct {
	Owner  string
	Repo   string
	Number int
}

func (p prRef) String() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", p.Owner, p.Repo, p.Number)
}

type prTooBigError struct {
	size int
}

func (e *prTooBigError) Error() string {
	return fmt.Sprintf("this pull request is big: its diff is over the %dKB limit, so review it in smaller pieces or paste the key files directly", maxPRDiffBytes/1024)
}

func parsePRURL(raw string) (prRef, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return prRef{}, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Host == "" && u.Scheme == "" {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return prRef{}, fmt.Errorf("invalid URL: %w", err)
		}
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return prRef{}, errors.New("expected a URL like https://github.com/owner/repo/pull/123")
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return prRef{}, errors.New("only github.com pull request URLs are supported")
	}
	m := prPathPattern.FindStringSubmatch(u.Path)
	if m == nil {
		return prRef{}, errors.New("expected a URL like https://github.com/owner/repo/pull/123")
	}
	number, err := strconv.Atoi(m[3])
	if err != nil {
		return prRef{}, fmt.Errorf("invalid pull request number: %w", err)
	}
	return prRef{Owner: m[1], Repo: m[2], Number: number}, nil
}

func (s *server) fetchPRDiff(ctx context.Context, ref prRef, token string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.githubAPI, ref.Owner, ref.Repo, ref.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("building GitHub request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("User-Agent", "grumpysenior")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching pull request diff: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errors.New("pull request not found: check the URL, or add a GitHub token if the repository is private")
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", errors.New("GitHub declined the request: invalid token or rate limit exceeded; add a GitHub token to raise the limit")
	default:
		return "", fmt.Errorf("GitHub returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPRDiffBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading pull request diff: %w", err)
	}
	if len(data) > maxPRDiffBytes {
		return "", &prTooBigError{size: len(data)}
	}
	diff := strings.TrimSpace(string(data))
	if diff == "" {
		return "", errors.New("pull request diff is empty")
	}
	return diff, nil
}

func countDiffStats(diff string) (files, additions, deletions int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return files, additions, deletions
}
