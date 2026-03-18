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
Charlie tests against    →  fake data that doesn't reflect production
Dave runs ALTER TABLE    →  everyone's app crashes
```

The shared development database is the single biggest source of friction in backend development. pgDelta fixes this permanently.

---

## How It Works

pgDelta gives every developer their own **isolated database branch** that mirrors their git branch automatically. Real production-shaped data. Zero risk to production. No shared dev DB.
```
┌──────────────────────────────────────────────────────┐
│  MAIN DB (read only — never touched by pgDelta)      │
│  users (1M rows) · orders (5M rows) · products       │
└─────────────────────┬────────────────────────────────┘
                      │ SELECT only (read replica safe)
                      ▼
┌──────────────────────────────────────────────────────┐
│  BRANCH DB (pgDelta owns this completely)            │
│                                                      │
│  branch_main          → base schema                  │
│  branch_feature_x     → Alice's isolated branch      │
│  branch_fix_payments  → Bob's isolated branch        │
└──────────────────────────────────────────────────────┘
```

---

## Installation

### macOS — Apple Silicon (M1/M2/M3)
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_macOS_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### macOS — Intel
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_macOS_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — amd64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_Linux_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — arm64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_Linux_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — Debian/Ubuntu (.deb)
```bash
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_linux_amd64.deb
sudo dpkg -i pgdelta_0.5.0_linux_amd64.deb
```

### Linux — RHEL/Fedora (.rpm)
```bash
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_0.5.0_linux_amd64.rpm
sudo rpm -i pgdelta_0.5.0_linux_amd64.rpm
```

### Build From Source
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
- **Anthropic API key** (optional) — for AI-powered query suggestions and risk detection

---

## Connecting To Your Database

### Step 1 — Set Up The Branch DB

pgDelta needs a **separate Postgres instance** it fully controls. Your main DB is never written to.

**Option A — Docker (fastest for local dev):**
```bash
docker run --name pgdelta-branches \
  -e POSTGRES_PASSWORD=pgdelta \
  -e POSTGRES_DB=branchdb \
  -p 5433:5432 \
  -d postgres:16
```

**Option B — Supabase free tier (for teams):**
```
1. Create new project at supabase.com
2. Use as branch DB
3. Copy connection string from Settings → Database
```

**Option C — Railway ($5/month, easy):**
```
1. New project at railway.app
2. Add Postgres service
3. Copy connection string from dashboard
```

**Option D — AWS RDS (enterprise):**
```
db.t3.micro → ~$15/month
Keep it separate from production RDS
```

---

### Step 2 — Get Your Connection Strings

Your connection strings follow this format:
```
postgres://username:password@host:port/database
```

**Common examples:**
```bash
# Local Postgres
postgres://localhost:5432/mydb

# AWS RDS
postgres://admin:pass@mydb.cluster-xxx.us-east-1.rds.amazonaws.com:5432/mydb

# Supabase
postgres://postgres:pass@db.xxxx.supabase.co:5432/postgres

# Railway
postgres://postgres:pass@containers-us-west-xxx.railway.app:5432/railway

# Neon
postgres://user:pass@ep-xxx.us-east-1.aws.neon.tech/mydb
```

---

### Step 3 — Create Read-Only User On Main DB (Recommended)

For maximum safety, create a dedicated read-only Postgres user for pgDelta:
```sql
-- Run this on your main database
CREATE USER pgdelta_readonly WITH PASSWORD 'your_secure_password';
GRANT CONNECT ON DATABASE mydb TO pgdelta_readonly;
GRANT USAGE ON SCHEMA public TO pgdelta_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgdelta_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO pgdelta_readonly;
```

Now use this user in your main DB URL — pgDelta physically cannot write to production.

---

### Step 4 — Set Environment Variables

Never put credentials directly in `.pgdelta.yml`. Use environment variables:
```bash
# Add to ~/.zprofile or ~/.bashrc
export MAIN_DATABASE_URL="postgres://pgdelta_readonly:pass@your-main-db:5432/mydb"
export BRANCH_DATABASE_URL="postgres://postgres:pgdelta@localhost:5433/branchdb"
export ANTHROPIC_API_KEY="sk-ant-..."   # optional, for AI features

# Reload
source ~/.zprofile
```

Or use a `.env` file (never commit this):
```bash
# .env
MAIN_DATABASE_URL=postgres://pgdelta_readonly:pass@your-db:5432/mydb
BRANCH_DATABASE_URL=postgres://postgres:pgdelta@localhost:5433/branchdb
ANTHROPIC_API_KEY=sk-ant-...
```
```bash
# Load it
source .env
```

Add to `.gitignore`:
```bash
echo '.env' >> .gitignore
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

Output:
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

This is the key step — pgDelta scans your schema and AI generates the right extraction queries for each table:
```bash
pgdelta configure
```

Output:
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

Review and customize the queries in `.pgdelta.yml` if needed. Commit it to share with your team.

### 3. Check Everything Is Working
```bash
pgdelta doctor
```

Output:
```
  ✓  .pgdelta.yml               config file found
  ✓  git repository             git repo detected
  ✓  git hooks                  all hooks installed
  ✓  main DB connection         PostgreSQL 16.x
  ✓  branch DB connection       PostgreSQL 16.x
  ✓  pgdelta schema             system tables present
  ✓  branch_main schema         base schema exists
  ✓  active branches            1 active branches
  ✓  AI (Anthropic)             ANTHROPIC_API_KEY set
  ✓  pgdelta in PATH            binary accessible

  10 passed · 0 warnings · 0 failed
  ✓  pgDelta is fully configured and ready.
```

### 4. Work Normally — Git Is Your Only Interface
```bash
git checkout -b feature-payments

# pgDelta silently:
# → creates branch_feature_payments schema on branch DB
# → copies schema from main
# → loads all configured tables in parallel with real data
# → you're ready to work with real production-shaped data

# Make schema changes
pgdelta migrate "ALTER TABLE users ADD COLUMN stripe_id TEXT"
# AI: ✓ safe — nullable column, no data impact

pgdelta migrate "CREATE INDEX idx_stripe ON users(stripe_id)"
# AI: ✓ safe

# Your app runs against real data in complete isolation
# No shared dev DB. No broken colleagues. No fake seeds.

git merge feature-payments
# pgDelta silently:
# → applies ALTER TABLE to parent branch
# → applies CREATE INDEX to parent branch
# → discards test data
# → cleans up branch schema
```

---

## The Complete Developer Workflow
```
Day 1 setup (5 minutes):

  pgdelta init           → connects your databases
  pgdelta configure      → AI generates snapshot queries
  commit .pgdelta.yml    → team shares config

Daily workflow (zero extra effort):

  git checkout -b feature-x
  → branch created automatically
  → real data loaded automatically

  [write code + migrations]

  git merge feature-x
  → DDL applied to parent automatically
  → test data discarded automatically
  → branch cleaned up automatically
```

---

## Configuration Reference
```yaml
# .pgdelta.yml

version: 1

# Main database — pgDelta reads from this only
main:
  url: ${MAIN_DATABASE_URL}      # use env vars, never hardcode credentials
  access: readonly

# Branch database — pgDelta owns this completely
branch_db:
  url: ${BRANCH_DATABASE_URL}
  access: readwrite

# How branching works
branching:
  schema_from: git-parent        # DB branch inherits schema from git parent branch
  auto_create: true              # create DB branch on git checkout -b
  auto_cleanup: true             # delete DB branch on git branch -d
  auto_merge: true               # apply DDL on git merge

# Data snapshot queries — generated by pgdelta configure
# Customize these for your use case
snapshots:
  default_row_limit: 10000
  chunk_size: 10000              # rows per streaming chunk
  parallel_tables: 3            # tables loaded simultaneously
  resume_on_interrupt: true     # Ctrl+C safe — resumes where it stopped
  tables:
    users:
      query: "SELECT * FROM users WHERE plan != 'free'"
      limit: 5000
    orders:
      query: "SELECT * FROM orders WHERE created_at > now() - interval '30 days'"
      limit: 10000
    products:
      query: "SELECT * FROM products"
  exclude:
    - audit_logs                 # never snapshot these tables
    - sessions
    - password_reset_tokens

# PII masking — sensitive columns never leave production
pii:
  auto_detect: true
  masks:
    users.email: "masked_{{id}}@example.com"
    users.phone: "+10000000000"

# AI features
ai:
  enabled: true
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}  # bring your own key
  features:
    extraction_suggestions: true  # suggests extraction queries
    migration_risk: true          # warns before dangerous DDL
    conflict_resolution: true     # explains merge conflicts
    pii_detection: true           # auto-detects sensitive columns

# Git integration
git:
  hooks: true                    # install post-checkout, post-merge, post-rewrite
  status_line: true              # show DB info in git status

# Anonymous telemetry — helps AI get smarter over time
# Only schema patterns sent, never actual data
telemetry:
  enabled: true
  anonymous: true
```

---

## Commands
```bash
# Setup
pgdelta init                           # initialize pgDelta in current repo
pgdelta configure                      # AI auto-generates snapshot config
pgdelta doctor                         # health check — diagnose issues
pgdelta doctor --fix                   # auto-fix detected issues

# Branching
pgdelta create <branch>                # create DB branch manually
pgdelta switch <branch>                # switch DB context
pgdelta delete <branch>                # drop branch schema + free storage
pgdelta delete <branch> --force        # skip unmerged migration warning
pgdelta list                           # list active branches
pgdelta list --all                     # include merged + deleted
pgdelta status                         # current branch details + divergence

# Data
pgdelta snapshot <table>               # snapshot single table (AI suggests query)
pgdelta snapshot <table> --no-ai       # skip AI suggestion
pgdelta snapshot --all                 # snapshot all configured tables in parallel
pgdelta url [branch]                   # output connection string for branch
pgdelta url [branch] --format=env      # output as DATABASE_URL=...
pgdelta url [branch] --format=json     # output as JSON

# Migrations
pgdelta migrate "<sql>"                # apply + record migration (AI risk check)
pgdelta migrate "<sql>" --no-ai        # skip risk analysis
pgdelta migrate "<sql>" --type=seed    # mark as seed data (travels to parent)

# Merging
pgdelta merge <branch>                 # merge DDL to parent branch
pgdelta merge <branch> --dry-run       # simulate only, no changes
pgdelta merge <branch> --strategy=theirs  # auto-resolve conflicts
pgdelta merge <branch> --strategy=ours    # skip conflicting migrations

# Rebasing
pgdelta rebase <branch>                # replay migrations on parent's latest
pgdelta rebase <branch> --onto main    # rebase onto specific branch
pgdelta rebase <branch> --dry-run      # simulate only

# CI/CD
pgdelta ci github                      # generate GitHub Actions workflow
pgdelta ci github --test-cmd "make test"  # custom test command
```

---

## AI Features

pgDelta uses Claude (Anthropic) to remove friction at every step. Set your API key to enable:
```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

**What AI does:**

**1. Extraction Query Generation**
When you snapshot a table, AI analyzes your branch name and schema to suggest the right data subset:
```
Branch: feature-payments
Table:  users

AI suggests:
SELECT * FROM users WHERE has_payment_method = true LIMIT 5000
```

**2. Migration Risk Detection**
Before applying any DDL, AI scans for dangers:
```
pgdelta migrate "ALTER TABLE users DROP COLUMN email"

AI: ⚠ WARNING
Dropping email is risky — it's likely used for authentication.
Found in codebase: src/auth/login.go:45, src/api/users.go:89
Recommend auditing before dropping.

Proceed anyway? [y/N]:
```

**3. Auto Schema Configuration**
`pgdelta configure` scans your entire schema and generates appropriate extraction queries for every table automatically.

**Enterprise:** Bring your own API key — data never sent to third parties.

---

## GitHub Actions Integration

Generate a complete CI workflow with one command:
```bash
pgdelta ci github --test-cmd "go test ./..."
# → writes .github/workflows/pgdelta.yml
```

Every PR automatically gets:
- Its own isolated database branch
- Real data loaded from your configured snapshots
- Tests run against that branch's DATABASE_URL
- Branch cleaned up when PR is closed

Add these secrets to your GitHub repo:
```
MAIN_DATABASE_URL    → your main DB (read replica recommended)
BRANCH_DATABASE_URL  → your branch DB
ANTHROPIC_API_KEY    → optional, for AI features
```

---

## Data Safety

pgDelta is designed for teams that care about data security:

| Guarantee | How |
|---|---|
| Main DB never written to | Read-only connection enforced in code |
| PII masked automatically | AI detects sensitive columns, masks on extraction |
| Data never leaves your infra | Self-hosted, no cloud dependency |
| AI key stays with you | Bring your own Anthropic key for enterprise |
| Full audit trail | Every extraction, migration, conflict logged |
| Resumable snapshots | Ctrl+C safe, picks up where it stopped |

---

## Why Not...

**Neon?** Cloud-only. Your data never leaves with pgDelta. No vendor lock-in.

**PlanetScale?** MySQL only. Schema-only branching — no data. Cut free tier.

**Dolt?** Rewrites Postgres storage engine. Slow, unfamiliar, niche adoption.

**Shared dev DB?** You already know why not.

---

## Architecture
```
Developer (git commands only)
    ↓ git hooks fire automatically
pgDelta CLI (Go — single binary, no runtime required)
    ├── Branch Engine     → create, switch, delete, list, status
    ├── Snapshot Engine   → lazy load via COPY protocol + keyset pagination
    ├── Migration Engine  → DDL auto-capture, sequences, merge, rebase
    ├── Conflict Engine   → simulate before apply, never blind merge
    ├── AI Engine         → Claude API — suggestions, risk, PII, conflicts
    ├── Git Engine        → hooks, parent detection, topology mirror
    └── Configure Engine  → AI schema scan, auto-generate yml config
    ↓
Main DB (read only)         Branch DB (pgDelta owned)
Your prod replica           One Postgres schema per branch
```

**Tech stack:** Go · Cobra · Chi · pgx v5 · SQLite · Goreleaser · Claude API

---

## Roadmap
```
v0.1  ✓  Core branching, lazy snapshots, migrations, merge, rebase
v0.2  ✓  pgdelta doctor — health checks
v0.3  ✓  snapshot --all — parallel loading
v0.4  ✓  pgdelta url + GitHub Actions integration
v0.5  ✓  pgdelta configure — AI auto-generates config from schema
v0.6  →  Web dashboard — branch visualization
v0.7  →  Team features — shared telemetry, org management
v1.0  →  Enterprise — SSO, audit exports, SLA, on-prem support
```

---

## Contributing
```bash
git clone https://github.com/ankeshk7/pgdelta
cd pgdelta
go mod tidy

# Requires Docker for Postgres test containers
go test ./... -timeout 180s

go build -o pgdelta .
./pgdelta --help
```

Issues and PRs welcome. Please open an issue before large changes.

---

## License

MIT — see [LICENSE](LICENSE)

---

Built by **Ankesh Kedia** — Senior Software Engineer

pgDelta was built to solve a real problem felt daily inside one of the world's largest financial institutions. If it works at JPMC scale, it works anywhere.

---

<p align="center">
  <strong>pgDelta Δ</strong><br>
  <a href="https://github.com/ankeshk7/pgdelta/releases">Releases</a> ·
  <a href="https://github.com/ankeshk7/pgdelta/issues">Issues</a> ·
  <a href="https://github.com/ankeshk7/pgdelta">GitHub</a>
</p>
