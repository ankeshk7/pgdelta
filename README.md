# pgDelta Δ

> **Git for your Postgres database.**
> If you know git, you already know pgDelta.

[![Release](https://img.shields.io/github/v/release/ankeshk7/pgdelta)](https://github.com/ankeshk7/pgdelta/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://golang.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-14+-336791.svg)](https://postgresql.org)

---

## The Problem

Every engineering team with a Postgres database has felt this:
```
Alice runs a migration   →  breaks Bob's feature branch
Bob seeds test data      →  pollutes the shared dev DB
Charlie tests against    →  fake data that misses real bugs
Dave runs ALTER TABLE    →  everyone's app crashes
```

The shared development database is the single biggest source of friction in backend development. pgDelta fixes this permanently — every developer gets their own isolated database branch with real production-shaped data, automatically.

---

## How It Works

pgDelta uses a **secondary database** for branches. Your main database is never touched — only read from.
```
┌──────────────────────────────────────────────────────┐
│  MAIN DB (read only — never written to by pgDelta)   │
│  Your real tables, your real data, untouched         │
└─────────────────────┬────────────────────────────────┘
                      │ SELECT only
                      ▼
┌──────────────────────────────────────────────────────┐
│  BRANCH DB (pgDelta owns this completely)            │
│  One Postgres schema per developer per branch        │
│                                                      │
│  branch_main          → base schema                  │
│  branch_feature_x     → Alice's isolated branch      │
│  branch_fix_payments  → Bob's isolated branch        │
└──────────────────────────────────────────────────────┘
```

---

## Installation

### macOS — Apple Silicon (M1/M2/M3/M4)
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_macOS_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### macOS — Intel
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_macOS_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — amd64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_Linux_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — arm64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_Linux_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux packages
```bash
# Debian / Ubuntu
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_linux_amd64.deb
sudo dpkg -i pgdelta_1.0.0_linux_amd64.deb

# RHEL / Fedora / CentOS
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.0_linux_amd64.rpm
sudo rpm -i pgdelta_1.0.0_linux_amd64.rpm
```

### Build from source
```bash
git clone https://github.com/ankeshk7/pgdelta
cd pgdelta
go build -o pgdelta .
sudo mv pgdelta /usr/local/bin/
```

---

## Prerequisites

- **Two Postgres instances** — one for your main DB (read only), one for branches
- **Git** — pgDelta integrates via git hooks
- **Anthropic API key** — optional, for AI-powered query suggestions and risk detection

---

## Connecting Your Database

### Step 1 — Set Up The Branch DB

pgDelta needs a separate Postgres instance it fully controls. Your main DB is never written to.

**Option A — Docker (fastest for local dev):**
```bash
docker run --name pgdelta-branches \
  -e POSTGRES_PASSWORD=pgdelta \
  -e POSTGRES_DB=branchdb \
  -p 5433:5432 \
  -d postgres:16
```

**Option B — Supabase free tier (great for teams):**
```
Create a new project at supabase.com
Use it as your branch DB
Copy the connection string from Settings → Database
```

**Option C — Railway ($5/month):**
```
New project at railway.app → Add Postgres
Copy connection string from dashboard
```

**Option D — AWS RDS (enterprise):**
```
db.t3.micro → ~$15/month
Keep it completely separate from your production RDS
```

---

### Step 2 — Create a Read-Only User On Main DB

For maximum safety, create a dedicated read-only Postgres user:
```sql
-- Run this on your main database
CREATE USER pgdelta_readonly WITH PASSWORD 'your_secure_password';
GRANT CONNECT ON DATABASE mydb TO pgdelta_readonly;
GRANT USAGE ON SCHEMA public TO pgdelta_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgdelta_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO pgdelta_readonly;
```

pgDelta physically cannot write to your main DB with this user.

---

### Step 3 — Set Environment Variables

Never put credentials directly in `.pgdelta.yml`. Use environment variables:
```bash
# Add to ~/.zprofile or ~/.bashrc
export MAIN_DATABASE_URL="postgres://pgdelta_readonly:pass@your-db:5432/mydb"
export BRANCH_DATABASE_URL="postgres://postgres:pass@localhost:5433/branchdb"
export ANTHROPIC_API_KEY="sk-ant-..."   # optional

source ~/.zprofile
```

Or use a `.env` file (add to `.gitignore`):
```bash
# .env — never commit this
MAIN_DATABASE_URL=postgres://pgdelta_readonly:pass@your-db:5432/mydb
BRANCH_DATABASE_URL=postgres://postgres:pass@localhost:5433/branchdb
ANTHROPIC_API_KEY=sk-ant-...
```
```bash
echo '.env' >> .gitignore
source .env
```

---

## Quickstart

### 1. Initialize
```bash
cd your-project

pgdelta init \
  --main-url "$MAIN_DATABASE_URL" \
  --branch-url "$BRANCH_DATABASE_URL"
```
```
  Connecting to main DB...    ✓  (PostgreSQL 16.x)
  Connecting to branch DB...  ✓  (PostgreSQL 16.x)
  Creating pgdelta schema...  ✓
  Creating branch_main...     ✓
  Writing .pgdelta.yml...     ✓
  Installing git hooks...     ✓

  pgDelta initialized successfully.
```

### 2. Auto-Configure Snapshot Queries

pgDelta scans your schema and AI generates the right extraction query for each table:
```bash
pgdelta configure
```
```
  Found 5 tables: users, orders, products, payments, audit_logs

  Asking AI to suggest extraction queries...  ✓

  users      SELECT * FROM users WHERE plan != 'free' LIMIT 5000
  orders     SELECT * FROM orders WHERE created_at >= NOW() - INTERVAL '30 days' LIMIT 10000
  products   SELECT * FROM products LIMIT 5000
  payments   SELECT * FROM payments WHERE status = 'completed' LIMIT 5000
  audit_logs SELECT * FROM audit_logs LIMIT 1000

  Write these queries to .pgdelta.yml? [Y/n]: Y
  ✓  .pgdelta.yml updated.
```

Tables are detected automatically from your real database — no hardcoding needed. Run `pgdelta configure` again whenever you add new tables.

### 3. Check Everything Is Working
```bash
pgdelta doctor
```
```
  ✓  .pgdelta.yml               config file found
  ✓  git repository             git repo detected
  ✓  git hooks                  all hooks installed
  ✓  main DB connection         PostgreSQL 16.x (read only)
  ✓  branch DB connection       PostgreSQL 16.x
  ✓  pgdelta schema             system tables present
  ✓  branch_main schema         base schema exists
  ✓  active branches            1 active branches
  ✓  AI (Anthropic)             ANTHROPIC_API_KEY set
  ✓  pgdelta in PATH            binary accessible

  10 passed · 0 warnings · 0 failed
  ✓  pgDelta is fully configured and ready.
```

### 4. Commit Config and Work Normally
```bash
git add .pgdelta.yml
git commit -m "chore: add pgDelta config"

# Now just use git — pgDelta handles everything else
git checkout -b feature-payments

# → branch created automatically
# → your real tables loaded in parallel
# → you're working with real production-shaped data
# → completely isolated from teammates
```

---

## Key Design Decisions

### Tables Detected Automatically — No Hardcoding

pgDelta reads your actual schema from the main DB. `pgdelta configure` detects every table and AI generates appropriate extraction queries. You never hardcode table names.
```bash
# Run once after init
pgdelta configure

# Run again when you add new tables
pgdelta configure
```

### Lazy Snapshots — Data Only When You Need It

Tables load on first query, not at branch creation. With pre-configured queries, loading is automatic and parallel.
```
git checkout -b feature-payments
→ users    5,000 rows loaded  (AI-suggested query)
→ orders   8,423 rows loaded
→ products   847 rows loaded
→ Ready in 18 seconds. Real data. No setup.
```

### Migrations Only On Merge

Only schema changes (DDL) travel to the parent on merge. Test data is discarded. Main's real data is never touched.
```sql
-- Travels to parent on merge:
ALTER TABLE users ADD COLUMN stripe_id TEXT;
CREATE INDEX idx_stripe ON users(stripe_id);

-- Discarded on merge (test data):
INSERT INTO users VALUES ('test@test.com', ...);

-- Explicitly mark as seed data to travel with DDL:
-- pgdelta:seed
INSERT INTO subscription_plans VALUES (1, 'pro', 49);
```

### Git-Native — Zero New Mental Model
```bash
git checkout -b feature-x   →  DB branch created automatically
git merge feature-x         →  DDL applied to parent automatically
git branch -d feature-x     →  DB schema dropped, storage freed
git rebase develop          →  migrations replayed on new base
```

---

## The Complete Developer Workflow
```bash
# Day 1 setup (5 minutes)
pgdelta init
pgdelta configure
git add .pgdelta.yml && git commit -m "chore: add pgDelta"

# Daily workflow (zero extra effort)
git checkout -b feature-stripe

# → branch created, 3 tables loaded in parallel, ready in seconds

pgdelta migrate "ALTER TABLE users ADD COLUMN stripe_id TEXT"
# AI: ✓ safe — nullable column, no impact

pgdelta migrate "CREATE INDEX idx_stripe ON users(stripe_id)"
# AI: ✓ safe

# Test your feature against real data
# Nobody else is affected — completely isolated

git merge feature-stripe
# → ALTER TABLE applied to parent
# → CREATE INDEX applied to parent
# → Test data discarded
# → Branch cleaned up
```

---

## Configuration Reference
```yaml
# .pgdelta.yml — commit this to share with team

version: 1

# Main database — pgDelta only reads from this
main:
  url: ${MAIN_DATABASE_URL}
  access: readonly

# Branch database — pgDelta owns this completely
branch_db:
  url: ${BRANCH_DATABASE_URL}
  access: readwrite

# Branching — mirrors git topology automatically
branching:
  schema_from: git-parent    # DB branch inherits from git parent
  auto_create: true          # create on git checkout -b
  auto_cleanup: true         # delete on git branch -d
  auto_merge: true           # apply DDL on git merge

# Snapshot queries — auto-generated by pgdelta configure
# Customize per table as needed
snapshots:
  default_row_limit: 10000
  chunk_size: 10000
  parallel_tables: 3
  resume_on_interrupt: true  # Ctrl+C safe — resumes where it stopped
  tables:
    users:
      query: "SELECT * FROM users WHERE plan != 'free'"
      limit: 5000
    orders:
      query: "SELECT * FROM orders WHERE created_at > now() - interval '30 days'"
      limit: 10000
  exclude:
    - audit_logs
    - sessions
    - password_reset_tokens

# PII masking — sensitive data never leaves production
pii:
  auto_detect: true
  masks:
    users.email: "masked_{{id}}@example.com"
    users.phone: "+10000000000"

# AI features — bring your own Anthropic API key
ai:
  enabled: true
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}
  features:
    extraction_suggestions: true
    migration_risk: true
    conflict_resolution: true
    pii_detection: true

# Git hooks — installed automatically on pgdelta init
git:
  hooks: true
  status_line: true

# Protected branches — prevent accidental merges
protect:
  - main
  - production

# Anonymous telemetry — helps AI get smarter
telemetry:
  enabled: true
  anonymous: true
```

---

## All Commands
```bash
# ── SETUP ──────────────────────────────────────────────
pgdelta init                          # initialize pgDelta in repo
pgdelta configure                     # AI auto-generate snapshot config
pgdelta doctor [--fix]                # 10 health checks + auto-fix
pgdelta team init                     # generate team-ready config
pgdelta team status                   # team branch activity overview

# ── BRANCHING ──────────────────────────────────────────
pgdelta create <branch>               # create DB branch
pgdelta clone <source> <new>          # clone branch with data + migrations
pgdelta switch <branch>               # switch DB context
pgdelta delete <branch>               # drop branch + free storage
pgdelta delete <branch> --force       # skip unmerged migration warning
pgdelta list                          # list active branches
pgdelta list --all                    # include merged + deleted
pgdelta status                        # current branch + divergence warning
pgdelta diff <b1> [b2]                # schema diff between branches
pgdelta diff <b1> [b2] --migrations   # migration diff instead of schema

# ── DATA ───────────────────────────────────────────────
pgdelta snapshot <table>              # snapshot single table (AI suggests)
pgdelta snapshot <table> --no-ai      # skip AI suggestion
pgdelta snapshot --all                # snapshot all configured tables
pgdelta url [branch]                  # connection string for branch
pgdelta url [branch] --format=env     # as DATABASE_URL=...
pgdelta url [branch] --format=json    # as JSON

# ── MIGRATIONS ─────────────────────────────────────────
pgdelta migrate "<sql>"               # apply migration + AI risk check
pgdelta migrate "<sql>" --no-ai       # skip risk analysis
pgdelta migrate "<sql>" --type=seed   # mark as seed data
pgdelta log [branch]                  # migration history (git log style)
pgdelta log [branch] --type=ddl       # filter by type
pgdelta reset [branch]                # undo last migration
pgdelta reset [branch] --steps=3      # undo last 3 migrations
pgdelta reset [branch] --force        # skip confirmation
pgdelta merge <branch>                # merge DDL to parent
pgdelta merge <branch> --dry-run      # simulate only
pgdelta merge <branch> --strategy=theirs  # auto-resolve conflicts
pgdelta rebase <branch>               # replay on parent's latest
pgdelta rebase <branch> --onto main   # rebase onto specific branch

# ── STASH & TAG ────────────────────────────────────────
pgdelta stash save [message]          # stash current migration state
pgdelta stash pop                     # restore last stash
pgdelta stash list                    # list all stashes
pgdelta stash drop [id]               # drop a stash
pgdelta tag create <name> [message]   # create named checkpoint
pgdelta tag list                      # list all tags
pgdelta tag delete <name>             # delete a tag

# ── PROTECT ────────────────────────────────────────────
pgdelta protect add <branch>          # protect branch from direct merge
pgdelta protect remove <branch>       # remove protection
pgdelta protect list                  # list protected branches

# ── AUDIT ──────────────────────────────────────────────
pgdelta audit                         # export all activity (table format)
pgdelta audit --format=csv            # CSV export
pgdelta audit --format=json           # JSON export
pgdelta audit --out=audit.csv         # write to file
pgdelta audit --branch=main           # filter by branch

# ── CI/CD ──────────────────────────────────────────────
pgdelta ci github                     # generate GitHub Actions workflow
pgdelta ci github --test-cmd "make test"

# ── DASHBOARD ──────────────────────────────────────────
pgdelta dashboard                     # open web dashboard
pgdelta dashboard --port=8080         # custom port
pgdelta dashboard --no-open           # don't auto-open browser
```

---

## AI Features

Set your Anthropic API key to unlock AI at every friction point:
```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

**Extraction query generation** — AI analyzes your branch name and table schema:
```
Branch: feature-payments
Table:  users

AI suggests:
SELECT * FROM users WHERE has_payment_method = true LIMIT 5000
```

**Migration risk detection** — AI warns before dangerous DDL:
```
pgdelta migrate "ALTER TABLE users DROP COLUMN email"

⚠ WARNING: Dropping email is risky.
  Likely used for authentication — could break login.
  Found in codebase: src/auth/login.go:45

Proceed anyway? [y/N]:
```

**Auto schema configuration** — `pgdelta configure` scans every table and generates queries automatically. No manual configuration needed.

**Enterprise:** Bring your own API key — data never sent to third parties.

---

## GitHub Actions Integration
```bash
pgdelta ci github --test-cmd "go test ./..."
# writes .github/workflows/pgdelta.yml
```

Every PR automatically gets:
- Its own isolated database branch
- Real data loaded from your snapshot config
- Tests run against that branch's `DATABASE_URL`
- Branch cleaned up when PR is closed

Add to your GitHub repo secrets:
```
MAIN_DATABASE_URL
BRANCH_DATABASE_URL
ANTHROPIC_API_KEY   (optional)
```

---

## Team Setup
```bash
# One-time team setup
pgdelta team init          # generates team-ready .pgdelta.yml
pgdelta configure          # AI generates snapshot queries
pgdelta protect add main   # protect main from accidental merges
pgdelta ci github          # generate CI workflow

git add .pgdelta.yml .github/workflows/pgdelta.yml
git commit -m "chore: add pgDelta team config"
```

Each team member then:
```bash
git clone your-repo
export MAIN_DATABASE_URL="..."
export BRANCH_DATABASE_URL="..."
pgdelta init
# Done — git hooks installed, ready to work
```

---

## Web Dashboard
```bash
pgdelta dashboard
# Opens http://localhost:7433
```

Shows all branches, migration history, snapshot status — live, auto-refreshing every 5 seconds.

---

## Audit Log
```bash
# View in terminal
pgdelta audit

# Export for compliance
pgdelta audit --format=csv --out=audit-2026-03.csv
pgdelta audit --format=json --out=audit.json

# Filter by branch
pgdelta audit --branch=main
```

Captures: branch creation, migration applied, snapshot completed, branch merged.

---

## Data Safety

| Guarantee | How |
|---|---|
| Main DB never written to | Read-only connection enforced in code + DB user |
| PII masked automatically | AI detects sensitive columns, masks on extraction |
| Data never leaves your infra | Self-hosted, no cloud dependency |
| AI key stays with you | Bring your own Anthropic key |
| Full audit trail | Every extraction, migration, conflict logged |
| Protected branches | Prevent accidental merges to production |
| Resumable snapshots | Ctrl+C safe — picks up where it stopped |

---

## Why Not...

**Neon?** Cloud-only. Your data never leaves with pgDelta. No vendor lock-in.

**PlanetScale?** MySQL only. Schema-only branching — no data. Cut free tier.

**Dolt?** Rewrites Postgres storage engine. Slow and unfamiliar workflow.

**Shared dev DB?** You already know why not.

---

## Architecture
```
Developer (git commands only)
    ↓ git hooks fire automatically
pgDelta CLI (Go — single binary, no runtime)
    ├── Branch Engine      create, clone, switch, delete, list
    ├── Snapshot Engine    lazy load via COPY protocol + keyset pagination
    ├── Migration Engine   capture, log, reset, merge, rebase
    ├── Conflict Engine    simulate before apply, never blind merge
    ├── AI Engine          Claude API — suggestions, risk, PII
    ├── Git Engine         hooks, parent detection, topology mirror
    ├── Protect Engine     branch protection + git hook enforcement
    ├── Stash/Tag Engine   checkpoint and restore migration state
    ├── Audit Engine       CSV/JSON activity log export
    ├── Team Engine        shared config generation
    └── Dashboard          GitHub-style web UI
    ↓
Main DB (read only)         Branch DB (pgDelta owned)
Your prod replica           One Postgres schema per branch
```

**Tech stack:** Go · Cobra · pgx v5 · SQLite · Goreleaser · Claude API

---

## Roadmap
```
v0.1  ✓  Core branching, lazy snapshots, migrations, merge, rebase
v0.2  ✓  pgdelta doctor — health checks
v0.3  ✓  snapshot --all — parallel loading
v0.4  ✓  pgdelta url + GitHub Actions
v0.5  ✓  pgdelta configure — AI auto-generates config
v0.6  ✓  Web dashboard — GitHub-style UI
v0.7  ✓  clone, diff, log, reset — complete git parity
v1.0  ✓  stash, tag, audit, protect, team — production ready
v1.1  →  Enterprise SSO + audit exports
v1.2  →  pgdelta cloud — hosted branch DB option
v2.0  →  Multi-database support (MySQL, SQLite)
```

---

## Contributing
```bash
git clone https://github.com/ankeshk7/pgdelta
cd pgdelta
go mod tidy

# Run tests (requires Docker)
go test ./... -timeout 180s

go build -o pgdelta .
./pgdelta --help
```

Issues and PRs welcome. Please open an issue before large changes.

---

## License

MIT — see [LICENSE](LICENSE)

---

Built by **Ankesh Kedia** — Senior Software Engineer.

pgDelta was built to solve a real problem felt daily inside large engineering organizations. If it works at enterprise scale, it works anywhere.

---

<p align="center">
  <strong>pgDelta Δ — v1.0.0</strong><br><br>
  <a href="https://github.com/ankeshk7/pgdelta/releases">Releases</a> ·
  <a href="https://github.com/ankeshk7/pgdelta/issues">Issues</a> ·
  <a href="https://github.com/ankeshk7/pgdelta">GitHub</a>
</p>
