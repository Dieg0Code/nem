---
name: nem
description: Version your conversation context with nem. Use it to RECALL prior context at the start of a session (decisions, resolved edge-cases, agreed conventions) and to PERSIST what you resolve, so context survives across sessions. nem is to your context what git is to code.
metadata:
  domain: workflow
  triggers: nem, context, remember, recall, persist, previous session, what did we decide, resume, continue prior work
---

# nem — version your own context

Your context window is wiped between sessions; nem's isn't. You (the agent) drive
it: recall at the start, persist what you resolve.

> **Prefer MCP tools if available.** If nem is connected as an MCP server, use the
> tools `nem_outline`, `nem_search`, `nem_read`, `nem_timeline`, `nem_status`,
> `nem_commit` instead of the CLI below — same capabilities, structured. The CLI
> is the fallback when MCP isn't wired.
>
> **Navigate, don't just keyword-search.** nem builds a tree (project → chat →
> commit), each node carrying a summary. Start with `outline` to see the map,
> reason about which branch fits your task, then drill in with `read`. This is
> reasoning-based navigation (PageIndex-style) — you find things by *meaning*, not
> by guessing exact keywords.

## 1. At the start: RECALL

The loop is: **`outline` (see the map) → reason → `read`/`search`/`timeline`
(drill in) → repeat.** nem returns small, token-bounded payloads on purpose.

- `nem status` — detected chat, staged messages, latest commit.
- `nem outline [--depth N]` — **start here.** The tree of project → chat → commit,
  each with a summary. Scan it, pick the branch that matches, then drill in. If
  there are durable **facts** (see below), they head the outline under
  `## Facts (always-loaded)` — read them first; that's who the user is.
- `nem search "<terms>" --mode hybrid --format llm` — retrieval that fuses BM25
  (messages + tree nodes) with **semantic embeddings** (when configured) and a
  recency boost, so it finds things by meaning even when your words don't match.
  It already matches **morphological variants** (Spanish/English stemming, so
  `programar` finds `programación`) and **expands by co-occurrence** (relevance
  feedback) in-process — so prefer a natural query and trust the first results.
  `--mode keyword` = exact terms only (no expansion); `--mode semantic` = vectors
  only; `--mode hybrid` (default) = everything. `--expand=false` turns off the
  relevance-feedback expansion. `--role all` also includes tools.
- `nem timeline <project|chatID>` — chronological evolution of a project/chat
  (how a decision changed over time; newest entries are current).
- `nem read <HEAD|hash|chat:id|commit:hash> --format llm` — the frozen snapshot of
  a commit or chat node. Read it before proposing anything: avoid redoing work or
  contradicting a prior decision.
- `nem log` — list context commits (hash + message).

> The tree comes from `nem index` (incremental: it reuses existing summaries and
> only computes what's new, so re-running after you commit is cheap). If `outline`
> looks stale or your fresh commits aren't in it, run `nem index`.

> **Scoped access:** you may be limited to a scope (via `NEM_SCOPE` or `--scope`).
> When scoped, `search`/`read`/`log` only see chats in that scope — so a "no
> results" doesn't mean a fact never existed, it may just be out of scope.
> `nem scope list` shows the available scopes.

> **Fix bad summaries.** If a node's summary is wrong, thin, or misleading, rewrite
> it: `nem annotate <nodeID> -m "<better summary>"` (MCP: `nem_annotate`). Node ids
> look like `project:foo`, `chat:id`, `commit:hash`. Your summary is *pinned* — it
> survives `nem index` and flows up into the project summary. This is the mutable
> layer over the immutable commits: the commit content never changes, but you curate
> how it's described and found.

### Durable facts — semantic memory (always loaded)

Commits are *episodic* memory: what happened in a conversation, retrieved by
relevance. **Facts** are *semantic* memory: durable truths the user states about
themselves — who they are, where they work, their routine, stable preferences.
They live in a privileged tier that loads at the **top of every `outline`**, so
they're never buried and never guessed by keyword search.

- `nem fact list` (MCP: `nem_fact` action=`list`) — read the durable facts.
- `nem fact add "<the fact>"` (MCP: `nem_fact` action=`add`, `text`) — assert one.
  Write a fact when the user tells you something durable about themselves or how
  they work — not conversation specifics (those are commits).
- **Reminders** (prospective memory) are facts with a date:
  `nem fact add "entregar notas" --due 2026-06-27` (MCP: `nem_fact` action=`add`,
  `due`). `due` accepts `YYYY-MM-DD`, `YYYY-MM-DD HH:MM`, `+3d`/`+2w`/`+6h`,
  `today`, `tomorrow`. They surface under `## Reminders` at the top of `outline`,
  sorted by date with a relative label (`in 3d`, `overdue 2d`). When one is
  handled, `nem fact done <id>` (MCP: `nem_fact` action=`done`, `id`) stops it
  surfacing (kept as trail).
- The layer is append-only: to update a fact that *changed*, don't delete it —
  `nem fact add "<new>" --supersedes <id>` keeps the old one as a trail.
  `nem fact rm <id>` is only for fixing a mistaken entry.
- Facts are **global** (not scope-filterable), so under an active `--scope`/
  `NEM_SCOPE` they're hidden and the `fact` commands refuse — unset the scope to
  reach them.

### Calibrate estimates against real history

Don't estimate effort in human-team units ("about two weeks"). Your real
throughput is recorded — use it. Before giving an estimate, check how long
analogous past work ACTUALLY took:

- `nem stats` — per-project **active time vs calendar span**, sessions, recency
  (a good start-of-session overview of what was worked on and how long).
- `nem timeline <project|chatID>` / MCP `nem_duration` — active time, sessions and
  last activity for one target.

Distinguish **active time** (real work) from **calendar span** (wall-clock, which
includes the user being away). Anchor your estimate in the *active* time of
similar tasks, not in generic timelines.

## 2. While working: IDENTIFY what's worth keeping

Persist only high-signal context: design decisions and their rationale,
edge-cases and how they're handled, agreed conventions, non-obvious fixes. Do NOT
save in-progress exploration, log dumps, or code that already lives in the repo.

## 3. When a thread is resolved: PERSIST

1. Stage the relevant messages:
   - `nem add -L <n>`  (last N messages)  or
   - `nem add --from <msgID> --to <msgID>`  (exact range).
2. `nem commit -m "<message you write yourself>"` — imperative, describing the
   DECISION, not the activity. Your future self will read it in `nem log`.
   e.g. `nem commit -m "store JSONL per commit; keep binary DB out of git"`.

### Choose which roles to stage (`--role`)

`nem add` and `nem search` take `--role` to control which message roles are
included. Valid roles: `user`, `assistant`, `reasoning`, `tool` (comma-separated),
or `all`.

- **Default** (no flag): `user,assistant,reasoning` — the high-signal content,
  excluding noisy `tool` output. This keeps snapshots small and readable.
- `--role assistant` — only your replies. `--role reasoning` — only your thinking.
- `--role all` — everything, including tool calls/outputs (use sparingly; it
  bloats the snapshot and burns tokens on read).

With `-L`, the count applies AFTER the role filter: `nem add -L 10 --role assistant`
stages your last 10 assistant messages, not 10 raw messages.

## 4. Sharing

`nem sync` (push to the team remote) is run by the **human**, not you. You persist
locally with add/commit; the user decides when to sync.

## Token economy

Prefer targeted `nem search` and `nem read <hash>` over dumping the whole log into
context. One commit per coherent decision: small snapshots search and read better.
If `nem status` shows no active session, pass `--chat <id>` to add/commit.
