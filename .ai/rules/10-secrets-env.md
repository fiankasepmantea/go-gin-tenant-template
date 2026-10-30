# Secrets and env files

Real env files (`.env`, `.env.development`, `.env.production`, `.env.testing`, `.env.local`, `.envrc`) belong to each developer/server and do not go into the repo.

## AI assistants must not

- Read, create, copy, or modify real env files. This includes via `cat`, `grep`, `echo >`, `cp`, `sed -i`.
- Show secret values in answers, logs, tests, or documentation.
- Use `git add -f` on ignored files, or `git commit --no-verify`.

## What is allowed

- Modify `*.example` (only variable names and safe example values).
- Add a new variable: add it to both `.example` files, to `internal/config/config.go` (+ validation + tests), and to `docs/environments.md`.
- Tell the developer to run `make env-init ENV=development` to create the local env file.

## Production

Production secrets come from a secret manager or a Kubernetes Secret (see `deployments/k8s/base/secret.example.yaml`), not from files in the repo. If a secret was ever committed, treat it as leaked: rotate the value, do not just delete the commit.
