<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.png">
  <img src="assets/logo.png" alt="mnemonics" width="300">
</picture>

# mnemonics

**Your agent forgets. `nem` doesn't.**

mnemonics versions agent context the way git versions code. The command is `nem`.

[![Go Report Card](https://goreportcard.com/badge/github.com/Dieg0Code/nem)](https://goreportcard.com/report/github.com/Dieg0Code/nem)
[![CI](https://img.shields.io/github/actions/workflow/status/Dieg0Code/nem/ci.yml?branch=main)](https://github.com/Dieg0Code/nem/actions)
[![codecov](https://codecov.io/gh/Dieg0Code/nem/graph/badge.svg?token=RFLOXG1BAA)](https://codecov.io/gh/Dieg0Code/nem)
[![Go Reference](https://pkg.go.dev/badge/github.com/Dieg0Code/nem.svg)](https://pkg.go.dev/github.com/Dieg0Code/nem)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/Dieg0Code/nem?display_name=release)](https://github.com/Dieg0Code/nem/releases)

</div>

---

> **mnemonics** · /nɪˈmɒnɪks/ · *noun* — a system for **encoding** information so
> it's easy to **recall**, by linking new, complex data to stable, familiar
> patterns the mind already holds onto. From Greek *mnēmōn* ("mindful"), after
> **Mnemosyne**, the goddess of memory.

That's the whole idea. Raw conversation is the complex, perishable data; an LLM
throws it away when it compacts its context window — the reasoning, the resolved
edge-cases, the decisions, gone. **mnemonics** encodes that context into stable,
recallable structure: **immutable commits**, a **navigable index tree**, and
**durable facts** — so any agent can bring it back later with the same commands
you would.

And because both Claude Code and Codex read and write the *same* store, they
stop being separate assistants with separate amnesia and start behaving like one
mind with one memory. Single binary, SQLite embedded in pure Go (no cgo),
offline.

## Install

```powershell
# Windows (Scoop)
scoop bucket add Dieg0Code https://github.com/Dieg0Code/scoop-bucket
scoop install nem
```
```bash
# macOS / Linux (Homebrew)
brew install Dieg0Code/homebrew-tap/nem

# or with Go
go install github.com/Dieg0Code/nem/cmd/nem@latest
```

## How your agent uses it

`nem init` installs a skill into Claude Code and Codex. The agent **recalls** at
the start of a session and **persists** what it resolves — no human in the loop:

```bash
nem outline                         # the map: facts + project → chat → commit
nem search "<terms>" --format llm   # hybrid search (BM25 + semantic)
nem read <hash> --format llm        # a frozen snapshot, clean for an agent
nem commit -m "decision about X"    # persist a resolved decision (immutable)
nem fact add "<who you are / how you work>"   # a durable fact, always recalled
```

The agent navigates the tree, reads what matters, and writes its own commits and
facts. You stay in control of one thing: `nem sync` (sharing) is yours.

## Commands

| | |
|---|---|
| `nem init` / `ingest` | set up `~/.nem`; pull in Codex & Claude Code sessions |
| `nem status` / `log` | active session; commit history |
| `nem add` / `commit` | stage messages; freeze them into an immutable snapshot |
| `nem fact` | durable facts + dated reminders (`--due`) — surfaced at the top of every session |
| `nem outline` / `timeline` | navigate the index tree; see how decisions evolved |
| `nem search` / `read` | hybrid retrieval (keyword/semantic); drill into content |
| `nem annotate` | rewrite a node's summary (pinned; survives re-index) |
| `nem index` | (re)build the tree — incremental, only computes what's new |
| `nem sync` / `clone` | push/pull to a git remote, **redacting secrets first** |
| `nem doctor` | check/install the optional pro deps |

## How it works

- **Two memories** — *episodic* (commits: what happened in a conversation,
  retrieved by relevance) and *semantic* (facts: durable truths about the user,
  loaded at the top of every `outline`, never guessed by search).
- **Immutable commits** — a commit copies the *text* of its range (a snapshot),
  not a pointer. What you saved never changes.
- **Navigable index** — a PageIndex-style tree (project → chat → commit) with
  summaries the agent reasons over before drilling in. The agent is the reranker.
- **Hybrid search** — BM25 (SQLite FTS5) over messages + nodes, fused (RRF) with a
  recency boost and optional semantic embeddings. In-process and self-contained:
  multilingual **stemming** (morphological variants match) and **relevance
  feedback** (co-occurrence expansion) lift recall without any external model.
- **Mutable summary layer** — `nem annotate` lets the agent curate how a commit is
  described and found, without touching the immutable content.

## Optional: the semantic layer

Richer LLM summaries and embeddings are opt-in and run locally (Ollama) or via an
OpenAI-compatible API — nem's core stays embedding-free and pure Go.

```bash
nem config set embed.backend ollama       # turn on the semantic layer
nem doctor --fix                          # installs Ollama + pulls the models
nem index                                 # build summaries + embeddings
```

MCP: `nem mcp` exposes the same capabilities as typed tools
(`nem_outline`, `nem_search`, `nem_read`, `nem_annotate`, …) for agents that
speak MCP.

## Security

Secret redaction runs **only on `nem sync`** — the one boundary where content
leaves your machine. API keys, tokens, connection strings, `Authorization`
headers and sensitive env vars are masked before anything is pushed, and reported:

```
$ nem sync
exported 12 commits
redacted 7 secrets: 5 huggingface-token, 2 env-secret
synced with the remote
```

## Development

```bash
go test ./...
go vet ./...
```

`-race` needs cgo; on Windows run from PowerShell (Git Bash DLL-shadows mingw).
CI runs race + coverage on Linux.

## License

[MIT](LICENSE)
