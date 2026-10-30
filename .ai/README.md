# AI folder

One place for all guidance read by AI assistants (Claude Code, Cursor, Gemini, Copilot) and new developers.
The source of truth is here; `CLAUDE.md` and `AGENTS.md` at the root only point to this folder.

| Folder | Contents | When to read |
|---|---|---|
| `rules/` | Mandatory rules and prohibitions. Must not be violated. | Always, before changing anything |
| `skills/` | Recipes for one type of task (how to create a module, write tests, etc.) | When the task matches |
| `plans/` | Roadmap and the plan for the current phase | When choosing what to work on next |

## How to use

1. Read `rules/00-must-follow.md` and `rules/10-secrets-env.md`.
2. Pick the skill that matches the task and follow its steps in order.
3. Check `plans/current-phase.md` so you do not work on anything outside the phase.
4. When done: run `make fmt vet test-race`, then update the plan if anything was completed.

## Maintaining this folder

- One file per topic, short (under ~120 lines), containing real commands and examples.
- New rules that come out of a bug or review: add them to `rules/`, not just to chat.
- Do not put secrets, tokens, or env file contents here.
