# GrumpySenior

An AI assistant, written in Go, that reviews code for **readability, structure, and maintainability** before it reaches human reviewers, so humans spend their review time on design and domain logic, not on catching resource leaks and SQL injection.

Built with the Go standard library only. No external dependencies.

## Architecture: one engine, two front doors

```
        CLI (default command)                    Web UI (serve subcommand)
  files / git diff / stdin                    browser → GET /  (embedded page)
            │                                 POST /review {code | pr_url, language, rewrite}
            │                                            │
            │                              GitHub PR diff fetch (github.com only,
            │                              optional X-GitHub-Token, 800KB cap)
            └────────────────┬───────────────────────────┘
                             ▼
                       review engine
              rubric catalogs (go:embed JSON)
              language detection (.go → go)
                             │
                             ▼
             Claude API (rubric-driven system prompt)
                             │
              optional second pass: --rewrite
        (≤5 high/medium findings, 3 concurrent requests)
                             │
                             ▼
                 structured JSON verdict
                             │
        ┌────────────────────┼────────────────────┐
        ▼                    ▼                    ▼
  pretty terminal      markdown table          raw JSON
  (local use)          (PR comment)            (CI / web UI)
```

Both front doors run the same engine: load the rubric catalogs for the detected language, build a system prompt around them, call the Claude API, and parse a strict-JSON verdict.

## The rubric

Every finding must name exactly one catalog entry (its smell), the canonical refactoring that fixes it when one is mapped, and the catalog's reference URL. Findings that fit no entry must be marked `uncatalogued` with a justification. The catalogs are embedded JSON, written in original wording and distilled from:

- `rubrics/smells.json`: 18 language-agnostic code smells in the five families (bloaters, oo-abusers, change-preventers, dispensables, couplers) cataloged at [Refactoring.Guru](https://refactoring.guru/refactoring/smells)
- `rubrics/refactorings.json`: 14 named refactoring techniques from the [Refactoring.Guru catalog](https://refactoring.guru/refactoring/techniques), each mapped to the smells it fixes
- `rubrics/go.json`: 15 Go-specific guidance entries distilled from [Effective Go](https://go.dev/doc/effective_go), the [Go Code Review Comments wiki](https://go.dev/wiki/CodeReviewComments), and the [Go Proverbs](https://go-proverbs.github.io/)

Language detection is by file extension (`.go` → the Go catalog is added; anything else gets the language-agnostic catalogs). In diff mode, the paths in the diff headers are inspected; a mixed batch loads the union of applicable rubrics.

## Setup

```bash
export ANTHROPIC_API_KEY=sk-ant-...   # get one at console.anthropic.com
go build -o grumpysenior .
```

## CLI usage

```bash
# Review files
./grumpysenior testdata/sample.go

# Review only what changed (pre-PR)
git diff main | ./grumpysenior --diff

# CI gate: exit code 1 if any high-severity finding
./grumpysenior --fail-on=high --format=json main.go

# Markdown output with suggested rewrites, ready to paste into a PR comment
./grumpysenior --format=markdown --rewrite handler.go
```

Exit codes: `0` clean or below threshold, `1` findings at or above `--fail-on`, `2` usage or runtime error.

### Flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--diff` | `false` | read a unified diff from stdin instead of files |
| `--format` | `pretty` | output format: `pretty` \| `json` \| `markdown` |
| `--model` | `claude-sonnet-4-6` | Claude model to use |
| `--fail-on` | `high` | minimum severity causing exit code 1: `high` \| `medium` \| `low` \| `never` |
| `--rewrite` | `false` | second pass: request minimal rewritten snippets for high/medium findings (capped at 5, 3 concurrent requests) |
| `--timeout` | `120s` | overall deadline for the review |
| `--version` | `false` | print version and exit |

## Web UI

```bash
./grumpysenior serve --addr 127.0.0.1:8787
```

Serves a single embedded page with two ways in:

- **GitHub PR** (primary): paste a `https://github.com/owner/repo/pull/123` URL and the server fetches the diff itself. Public repos need no setup; private repos need a GitHub token (sent per request as `X-GitHub-Token`, never stored). Only strictly parsed `github.com` PR URLs are fetched, so the server never requests an arbitrary user-supplied address. Diffs are capped at 800KB; anything bigger is flagged as a **big PR** with advice to split it or paste key files, rather than producing a shallow review.
- **Paste code**: a textarea with a language selector, as before.

Results render as a review grade ring, severity count tiles, PR stats (files changed, additions/deletions), findings as severity-colored cards with smell/refactoring pills, reference links, and copyable rewrite snippets, plus an **Anthropic usage strip** showing input/output tokens and request count for the run, taken from the API's own usage reporting. `POST /review` accepts `{"code" | "pr_url", "language", "rewrite"}` and returns the Review JSON extended with a `meta` object (source, PR stats, detected language, token usage).

Each user brings their own key, either provider works: an Anthropic key (sent per request as `X-API-Key`) or a Gemini key (sent as `X-Gemini-Key`). The key is used for that one review and never stored or logged; it lives only in the browser tab's memory. If both keys are set, Claude runs the review. When no header is present, the server falls back to its own `ANTHROPIC_API_KEY`, then `GEMINI_API_KEY`, environment variables; if nothing is available the request gets a 401. Key fields are masked with block characters and show a live **keyprint** (the first bytes of the key's SHA-256, like an SSH fingerprint) so users can confirm the key registered without ever seeing it on screen. The results header shows which provider and model ran the review.

`serve` flags: `--addr` (default `127.0.0.1:8787`), `--model` (Claude), `--gemini-model`, `--timeout`, `--trust-proxy`.

### Deploying publicly

The web UI can be run as a public bring-your-own-key service. The rules that make that safe:

- **Never set `ANTHROPIC_API_KEY` or `GEMINI_API_KEY` in production.** With no server-side keys, every visitor must supply their own key, so strangers cannot spend your API budget. The server then holds no secrets at all.
- **Always terminate TLS in front of it** (Fly.io, Render, or Caddy on a VPS). Users type API keys into this page; plain HTTP is not acceptable.
- **Run with `--trust-proxy`** when behind a reverse proxy or PaaS, so per-IP rate limiting uses the real client address from `X-Forwarded-For` (rightmost hop) instead of lumping every user into the proxy's IP. Leave it off when clients connect directly, otherwise the header would be spoofable.

A production-ready `Dockerfile` is included (distroless, nonroot, ~15MB image, listens on `0.0.0.0:8787` with `--trust-proxy` on):

```bash
docker build -t grumpysenior .
docker run -p 8787:8787 grumpysenior

# or on Fly.io
fly launch && fly deploy
```

**Security note:** when running locally, `serve` binds to localhost by default. If you share an instance with trusted teammates, put it behind HTTPS, because API keys typed into a plain-HTTP page can be read in transit. There is no authentication beyond the API key itself; request bodies are capped at 200KB and reviews are rate-limited to 10 per minute per IP. Only run or use instances operated by someone you trust with your key.

## Tests

```bash
go test ./...        # unit tests, no network
go test -race ./...  # same, with the race detector
```

Covers the rubric catalogs (valid JSON, refactoring cross-references, prompt compactness), JSON parsing (including markdown-fenced responses), severity gating and ordering, renderer output, the two-pass rewrite flow against a fake API (request counts, concurrency cap, context cancellation), and the HTTP handlers (happy path, oversized bodies, rate limiting).

### Evaluation harness

`testdata/golden/` holds one deliberately flawed file per seeded smell (long method, primitive obsession, duplicate code, SQL injection, goroutine leak). The live evaluation suite reviews each one against the real API and asserts the expected smell is reported:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go test -tags eval ./...
```

It is skipped automatically when `ANTHROPIC_API_KEY` is not set, and a plain unit test keeps every fixture valid Go.

## Try the demo

`testdata/sample.go` is deliberately flawed (SQL injection, ignored errors, unclosed rows, unescaped JSON output). Run the reviewer against it to see high-severity findings with concrete fixes.

## Design decisions

- **Stdlib only.** Trivially auditable, builds anywhere Go builds; `go.mod` has no `require` lines.
- **Rubric as data, not prose.** The catalogs are embedded JSON, so the prompt, the renderers, and the eval harness all share one vocabulary of smell and refactoring names.
- **Structured output over free text.** Findings are data, so they can gate CI, populate PR comments, or feed dashboards.
- **Severity threshold as exit code.** Makes it a drop-in pre-review step in any pipeline (`--fail-on=high`).
- **Noise rules in the prompt.** The biggest failure mode of AI reviewers is nitpicking; the rubric explicitly forbids anything gofmt or a linter would catch.
- **Rewrites are opt-in and bounded.** The second pass costs extra API calls, so it is behind `--rewrite`, capped at 5 findings, and limited to 3 concurrent requests with first-error cancellation.

---
