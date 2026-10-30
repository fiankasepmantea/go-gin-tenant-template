# Security Policy

## What must never be committed
- `.env`, `.env.*` (only `*.example` files are allowed), `PASETO_KEY`, database passwords
- Private keys and certificates: `*.pem`, `*.key`, `*.p12`, `id_rsa*`
- Cloud and cluster credentials: `kubeconfig`, `*-secret.yaml`, service-account JSON, `*.tfstate`, `*.tfvars`
- Database dumps, backups, uploads, logs

`.gitignore` only stops accidental commits. It does not hide anything from people or tools that already have access to your machine or repository.

## Layers of protection
1. **Production secrets never live on developer machines.** Keep them in GitHub Actions Secrets, a cloud secret manager, Vault or Sealed Secrets. Developers and AI tools only hold `.env.development` with throwaway values.
2. **Pre-commit hook** (`make hooks`): blocks env files and private keys, then runs `gitleaks` on staged changes.
3. **CI** (`env-guard`, `secret-scan`): rejects committed env files and scans the full history with gitleaks.
4. **Repository settings** (do these on GitHub): private repo, branch protection on `main`, required reviews, CODEOWNERS, secret scanning with push protection.
5. **AI tool rules** (`.cursorignore`, `.geminiignore`, `.claude/settings.json`, `.ai/rules/10-secrets-env.md`) are advisory. They reduce accidents, they are not a security boundary.

## If a secret was committed
Deleting the file is not enough, the value stays in Git history. Rotate it first (new password, new key, revoke the token), then clean the history if needed.

## Freelance and client work
- Keep this template in your own private repo and give each client a separate copy.
- Hand the code over in the client's repository. The client fills production secrets in their own secret manager.
- Ownership and licensing belong in the contract; source code you hand over cannot be hidden technically.

## Reporting a vulnerability
Open a private security advisory on the repository (Security tab), or contact the maintainer directly. Do not open a public issue.
