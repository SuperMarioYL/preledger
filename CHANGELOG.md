# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-10-20

First release: capture, review, and refutation-to-test compilation for Claude Code.

### Added

- `preledger install` — wires the Claude Code PostToolUse hook (`Edit|Write`) into the project's `.claude/settings.json` and scaffolds the `.preledger/` ledger directory; idempotent.
- `preledger hook` — capture pipeline: reads the hook payload from stdin, records the mechanical per-edit premise, layers heuristic extraction (new dependencies, missing config/env keys, git-new paths) over the transcript context, and appends entries to the append-only ledger at `.preledger/ledger.jsonl`. Capture failures never block an edit (exit 0).
- `preledger up` — the local zh-first review UI at 127.0.0.1:7830 (embedded via go:embed): entries grouped by edit, status filters, per-entry confirm/refute with verbatim transcript quotes, fsnotify-driven live updates over SSE.
- Optional LLM triage via DeepSeek or GLM OpenAI-compatible endpoints (`PRELEDGER_TRIAGE_PROVIDER` / `PRELEDGER_TRIAGE_API_KEY`); degrades to the offline heuristics on any failure.
- Refutation compiler: a refuted premise compiles into a real failing test for Go repos (`go test`) and package.json repos (vitest); premises without a mechanical check compile into TODO stubs carrying the premise as a comment.
- Deterministic assumption IDs (`hash(edit ref + statement)`), append-only JSONL ledger, statuses `open | confirmed | refuted` resolved by appending records.
- Demo artifacts: `assets/demo.gif` (real vhs recording), `docs/demo.tape`, `examples/demo/setup.sh` (generates the sample repo and hook payloads), and `docs/demo-results.json` (recorded run).
- CI (build/vet/test), goreleaser-based release pipeline for linux/darwin/windows × amd64/arm64, and a manual workflow to re-render the demo GIF.

[0.1.0]: https://github.com/SuperMarioYL/preledger/releases/tag/v0.1.0
