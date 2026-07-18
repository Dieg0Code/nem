---
name: nem
description: Version your conversation context with nem. Use it to RECALL prior context at the start of a session (decisions, resolved edge-cases, agreed conventions) and to PERSIST what you resolve, so context survives across sessions, agents, and teammates. nem is to your context what git is to code.
metadata:
  domain: workflow
  triggers: nem, context, remember, recall, persist, previous session, what did we decide, resume, continue prior work, team memory, shared context
---

# nem — version your own context

Your context window is wiped between sessions; nem's isn't. You drive it: **recall**
what was resolved before you start, **persist** what you resolve. It's git for context.

> **Prefer the MCP tools when connected** (`nem_outline`, `nem_search`, `nem_read`,
> `nem_timeline`, `nem_status`, `nem_commit`, `nem_fact`, `nem_annotate`) — same
> capabilities, structured. The CLI below is the fallback.

## Recall — at the start of a session

The loop is **`outline` → reason → `read`/`search` → repeat**. Payloads are
token-bounded by design; navigate by meaning, don't dump.

- **`nem outline`** — start here. The map of project → chat → commit, each with a
  summary, with durable **facts** and **reminders** at the top. Scan it, pick the
  branch that fits, drill in.
- **`nem search "<terms>"`** — hybrid retrieval (BM25 + semantic + recency) that
  finds by meaning, not exact keywords. Trust a natural query and the first results.
- **`nem read <HEAD|hash|chat:id|commit:hash>`** — the frozen, immutable snapshot.
  Read it before proposing anything, so you don't redo work or contradict a past
  decision.
- **`nem status` · `nem timeline <project|chatID>` · `nem log`** — session state
  (including uncommitted messages and nearby parallel sessions) · how a decision
  evolved · the commit list.

The tree is built by `nem index` (incremental and cheap). If `outline` looks stale
or your fresh commits aren't in it, run `nem index`. If a node's summary is wrong,
rewrite it with `nem annotate <nodeID> -m "<better>"` — it's pinned and survives
re-indexing.

**Curation reflexes — every session leaves the store better than it found it:**

- If a summary led you to read a commit that wasn't what it promised, fix it with
  `nem annotate` **before moving on**. You just paid the cost of the bad
  description; you're the best-placed agent that will ever see it.
- If you discover a committed decision was later reversed, annotate the old node:
  `nem annotate <nodeID> -m "SUPERSEDED by <new hash>: <what changed>"` — so
  search stops sending future agents to dead decisions.
- `search` → `read` pairs **train the ranking**: reading a result you found via
  search teaches nem that this query leads there, and boosts it next time. So
  search first, then read — it's not just recall, it sharpens the store.
- If `outline` shows the **revision-health warning** (nothing superseded in a
  long time), treat it as an echo-chamber smell: this session, pick one recalled
  decision and genuinely test it against current reality — supersede it if it no
  longer holds, and tell the user you did. Recall without critique is just echo.

## Two kinds of memory

- **Commits — episodic.** What happened in a thread, retrieved by relevance.
- **Facts — semantic.** Durable truths the user states about themselves: who they
  are, where they work, their routine, stable preferences. They load at the **top
  of every `outline`**, so they're never buried or guessed at.
  - `nem fact add "<the fact>"` / `nem fact list`. Write a fact when the user tells
    you something durable about *themselves or how they work* — not thread
    specifics (those are commits).
  - **Reminders** are facts with a date: `nem fact add "ship notes" --due 2026-06-27`
    (`+3d`, `tomorrow`, …). They surface under `## Reminders`; `nem fact done <id>`
    retires one.
  - The layer is append-only: to update a fact that changed,
    `nem fact add "<new>" --supersedes <id>` (keeps the trail). `nem fact rm <id>`
    is only for fixing a mistaken entry.
  - **Facts are knowledge, NOT a task tracker.** Backlog items, TODOs and
    feature ideas belong wherever the project already tracks work (its issue
    tracker, a TODO file, a board — whatever exists), not in `fact add` —
    stable facts compete for a small always-loaded budget, and a task would
    burn a slot in every future session. A *known limitation* is a fact;
    *"build the fix"* is a work item (link it from the fact if both exist).
    Reminders (`--due`) are for time-boxed revisits like decision reviews,
    not open-ended work.

> Estimate from real history, not human-team units. `nem stats` and
> `nem timeline <target>` record your **active time** (real work) vs **calendar
> span** (wall-clock). Anchor any effort estimate in the active time of similar
> past work.

## Persist — when a thread is resolved

Keep only high-signal context: decisions and their rationale, edge-cases and how
they're handled, agreed conventions, non-obvious fixes. Not in-progress
exploration, log dumps, or code that already lives in the repo.

Default path: **`nem close -m "<the decision, imperative>"`**. It ingests fresh
agent logs, commits the contiguous new messages since HEAD, and refreshes the
index. Use it when you are ending a coherent thread or recovering after another
agent ran in parallel.

Manual path for curated snapshots:

1. Stage: `nem add -L <n>` (last N messages) or `nem add --from <id> --to <id>`.
2. Commit: `nem commit -m "<the decision, imperative>"` — describe the DECISION,
   not the activity, e.g. `"store JSONL per commit; keep the binary DB out of git"`.

`--role` controls which message roles you stage (`user,assistant,reasoning` by
default; `all` adds noisy tool output). With `-L`, the count applies after the role
filter. `nem close` refuses to run if manual staging already exists, so it never
silently folds curated staged messages into an automatic close.

## The decision journal — the loop that makes the human smarter

Memory that only accumulates makes the user faster; memory that gets **confronted
with outcomes** makes them better. Three reflexes close that loop, and none of
them depends on anyone's good intentions — the structure does the work:

1. **Decision → dated expectation.** When you persist a significant decision,
   also record what it's expected to deliver, and schedule the confrontation:
   `nem fact add "review <hash>: did <decision> actually <expected result>?" --due +30d`.
   A decision without a falsifiable expectation is dogma; give it one.
2. **Reminder fires → verdict recorded.** When a review reminder surfaces, don't
   just `fact done` it — actually check how it turned out, and write the verdict
   on the commit's node: `nem annotate <nodeID> -m "OUTCOME: worked / failed
   because <reality>"`. Over time the store separates decisions that were
   *validated*, *refuted*, or *never confronted* — that last bucket is the
   user's self-deception meter; say so if it grows.
3. **Dissent gets committed, not smoothed over.** If you recommended X, the user
   chose Y, and the work proceeds — commit the disagreement as a recorded bet,
   not a reproach: `nem commit -m "dissent: recommended X (<why>); user chose Y
   (<their why>)"`. When the outcome lands (reflex 2), both sides get calibrated.
   An empty dissent log after months of use means the agents are just agreeing —
   sycophancy confirmed by data. Disagreeing for the record is a service, not a
   conflict.

## Team memory — shared context

A team store is a common memory base a whole team pushes curated decisions and
facts to. **Recall is already federated**: `outline`, `search` and `read` span your
personal store *plus* every team store, tagging each hit by origin (`[team:<name>]`)
and its author — so before starting, you can see if a teammate already resolved it.

When something is genuinely *shared* knowledge — a team decision, a convention —
persist it there instead of (or after) your personal store:

- `nem commit --team <name>` · `nem fact add --team <name>` — write to the team.
- `nem commit promote <hash> --team <name>` — lift an existing personal commit up.
- `--local` / `--team <name>` narrow a federated read to one place.

Default to your personal store; promote to a team deliberately. **`nem sync` /
`nem team sync` publish to a git remote — content leaves the machine.** You can run
them, but confirm with the user first and recommend the remote stay private;
whether you sync on your own or always ask first is theirs to decide.

When you create that remote for them, seed its root with a short, branded
`README.md` so it isn't a bare dump — and **write it in the user's main language,
in a fun, cool, hacker-culture voice that makes them feel good about what they're
building**, not corporate filler. Include: the nem logo as a theme-adaptive
`<picture>` (light `…/Dieg0Code/nem/main/assets/logo.png`, dark
`…/assets/logo-dark.png`), one line on what the repo is (versioned agent memory via
`nem sync`, secrets redacted), a personal touch drawn from what you know about them,
and a friendly note that *you generated it automatically — delete anytime*. Commit
it once at the repo root; sync only manages `.gitignore` and `store/`, so it stays
untouched.

## Token economy

Prefer targeted `search` + `read <hash>` over dumping a whole log into context. One
commit per coherent decision: small snapshots search and read better. If `nem
status` shows no active session, pass `--chat <id>` to add/commit. Under an active
`--scope`/`NEM_SCOPE`, reads see only that scope and facts are hidden — a "no
results" may just be out of scope (`nem scope list`).
