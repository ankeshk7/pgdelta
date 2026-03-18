# pgDelta Δ

> **Git for your Postgres database.**  
> If you know one, you know both.

[![Release](https://img.shields.io/github/v/release/ankeshk7/pgdelta)](https://github.com/ankeshk7/pgdelta/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://golang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791.svg)](https://postgresql.org)

---

## The Problem

Every engineering team with a Postgres database has felt this:

\`\`\`
Alice runs a migration   →  breaks Bob's feature branch
Bob seeds test data      →  pollutes the shared dev DB
Charlie tests against    →  fake data that doesn't reflect production
Dave runs ALTER TABLE    →  everyone's app crashes
\`\`\`

The shared development database is the single biggest source of friction in modern backend development. pgDelta fixes this. Every developer gets their own isolated database branch — with real, production-shaped data — that mirrors their git branch automatically.

---

## How It Works

pgDelta uses a **secondary database** for branches. Your production database is never touched — only read from.

\`\`\`
┌─────────────────────────────────────────────────┐
│  MAIN DB (read only — never written to)         │
│  users (1M rows) · orders (5M rows) · products  │
└──────────────────────┬──────────────────────────┘
                       │ SELECT only
                       ▼
┌─────────────────────────────────────────────────┐
│  BRANCH DB (pgDelta owns this)                  │
│  branch_main          → base schema             │
│  branch_feature_x     → Alice's isolated env    │
│  branch_fix_payments  → Bob's isolated env      │
└─────────────────────────────────────────────────┘
\`\`\`

---

## Key Design Decisions

### Lazy Snapshots — Data Only When You Need It

Tables don't exist in a branch until you query them. When you do, pgDelta prompts for an extraction query, runs it against main, and streams results in via COPY protocol.

### AI-Native — Not An Afterthought

- **Extraction query suggestions** — AI analyzes branch name + schema to suggest the right data subset
- **Migration risk detection** — AI warns before dangerous DDL
- **Conflict resolution hints** — AI explains merge conflicts in plain English
- **PII auto-detection** — AI scans schema on init and flags sensitive columns

### Git-Native — Zero New Mental Model

\`\`\`bash
git checkout -b feature-payments
# → DB branch created automatically

git merge feature-payments
# → DDL migrations applied to parent

git branch -d feature-payments
# → Schema dropped, storage freed
\`\`\`

### Migrations Only On Merge

Only schema changes (DDL) travel to the parent on merge. Test data is discarded. Main's real data is never touched.

---

## Installation

\`\`\`bash
# macOS Apple Silicon
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_Darwin_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/

# macOS Intel
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_Darwin_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/

# Linux amd64
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_Linux_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
\`\`\`

---

## Quickstart

\`\`\`bash
cd your-project

pgdelta init \
  --main-url "postgres://user:pass@localhost:5432/mydb" \
  --branch-url "postgres://user:pass@localhost:5433/branches"

# Output:
#   Connecting to main DB...    ✓
#   Connecting to branch DB...  ✓
#   Creating pgdelta schema...  ✓
#   Installing git hooks...     ✓
#   pgDelta initialized.
\`\`\`

After init, just use git normally. pgDelta handles the rest.

---

## Commands

\`\`\`bash
pgdelta init                           # Initialize in current repo
pgdelta create <branch>                # Create a DB branch manually
pgdelta switch <branch>                # Switch DB context
pgdelta delete <branch>                # Drop branch schema
pgdelta list                           # List all branches
pgdelta status                         # Current branch + divergence warnings
pgdelta snapshot <table>               # Snapshot a table (AI suggests query)
pgdelta migrate "<sql>"                # Apply + record a migration (AI risk check)
pgdelta merge <branch>                 # Merge DDL to parent
pgdelta merge <branch> --dry-run       # Simulate only
pgdelta rebase <branch> --onto <base>  # Replay migrations on new base
\`\`\`

---

## Configuration

\`\`\`yaml
# .pgdelta.yml
version: 1

main:
  url: \${MAIN_DATABASE_URL}
  access: readonly

branch_db:
  url: \${BRANCH_DATABASE_URL}
  access: readwrite

branching:
  schema_from: git-parent
  auto_create: true
  auto_cleanup: true
  auto_merge: true

snapshots:
  default_row_limit: 10000
  tables:
    users:
      query: "SELECT * FROM users WHERE plan != 'free'"
      limit: 5000
    orders:
      query: "SELECT * FROM orders WHERE created_at > now() - interval '30 days'"

ai:
  enabled: true
  provider: anthropic
  api_key: \${ANTHROPIC_API_KEY}
\`\`\`

---

## Why Not...

**Neon?** Cloud-only. Your data never leaves with pgDelta.

**PlanetScale?** MySQL only. Schema-only branching. No data.

**Dolt?** Rewrites Postgres storage engine. Slow, unfamiliar.

**Shared dev DB?** You already know why not.

---

## Architecture

\`\`\`
Developer (git commands only)
    ↓ git hooks
pgDelta CLI (Go — single binary)
    ├── Branch Engine     → create, switch, delete, list
    ├── Snapshot Engine   → lazy load via COPY + keyset pagination
    ├── Migration Engine  → DDL capture, merge, rebase
    ├── Conflict Engine   → simulate before apply, never blind merge
    ├── AI Engine         → Claude API
    └── Git Engine        → hooks, parent detection
    ↓
Main DB (read only)   Branch DB (pgDelta owned)
\`\`\`

Tech: Go · Cobra · Chi · pgx v5 · SQLite · Goreleaser

---

## Roadmap

\`\`\`
v0.1  ✓  Core branching, snapshots, migrations, merge, rebase, AI
v0.2  →  GitHub Actions (per-PR database environments)
v0.3  →  pgdelta doctor (health checks)
v0.4  →  Web dashboard
v0.5  →  Team features
v1.0  →  Enterprise (SSO, audit exports, on-prem)
\`\`\`

---

## Contributing

\`\`\`bash
git clone https://github.com/ankeshk7/pgdelta
cd pgdelta
go mod tidy
go build -o pgdelta .
./pgdelta --help
\`\`\`

---

## License

MIT — see [LICENSE](LICENSE)

---

Built by **Ankesh Kedia** — Senior Software Engineer, JPMC Asset & Wealth Management.

pgDelta was built to solve a real problem felt daily inside one of the world's largest financial institutions. If it works at JPMC scale, it works anywhere.
