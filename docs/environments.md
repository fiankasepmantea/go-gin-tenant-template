# Development and production modes

The mode is selected via `APP_ENV` (`development` is the default, `production`, `testing`). Other values are rejected.

| | development | production |
|---|---|---|
| Env file | `.env.development` | `.env.production` or process environment (recommended) |
| Log | text, `debug` level | JSON, `info` level |
| Gin | debug mode | release mode |
| `DB_SSLMODE` | `disable` allowed | `disable` **rejected** (use `require`/`verify-full`) |
| `DB_PASSWORD` | may be empty | **required** |
| Access token | 1 hour (comfortable while coding) | 15 minutes |
| Secrets | generated locally by `make env-init` | from Kubernetes Secret / secret manager |

## Configuration priority order

1. process environment (Kubernetes ConfigMap/Secret, `export`, CI)
2. `.env.<mode>`
3. `.env` (local fallback)

The files are not required to exist. In production containers usually only the process environment is used.

## Usage

```bash
make hooks                    # once per clone: reject commits of env files
make env-init                 # create .env.development + a new PASETO_KEY (does not overwrite)
make docker-up                # Postgres + Redis using .env.development
make run                      # development mode
make env-init ENV=production  # for local testing only; on the server fill via the secret manager
make run ENV=production
```

## Env file rules

Only `.env.development.example` and `.env.production.example` may be committed (without secret values).
`.env`, `.env.development`, `.env.production`, and all other variants are ignored by git.

Layered safeguards. `.gitignore` only prevents files from being **tracked** by git; it does not stop anyone from creating files on their own computer.

| Layer | What it prevents |
|---|---|
| `.gitignore` | `git add .` does not include env files |
| `scripts/hooks/pre-commit` (`make hooks`) | commit is rejected if an env file/private key is staged, even with `git add -f` |
| Job `env-guard` in CI | PR is rejected if an env file is committed (hooks can be bypassed with `--no-verify`, CI cannot) |
| `.claude/settings.json` | Claude Code is forbidden from reading/modifying certain env files |
| `.cursorignore`, `.geminiignore` | Cursor/Gemini CLI do not read/index env files (support depends on the tool) |
| `CLAUDE.md` | instructions for other AI assistants |

If a secret was ever committed: treat it as **leaked**. Rotate its value; removing the file from git history alone is not enough.
