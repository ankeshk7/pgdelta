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

The shared development database is the single biggest source of friction in backend development. pgDelta fixes this permanently.

---

## How It Works

pgDelta gives every developer their own isolated database branch that mirrors their git branch automatically. Real production-shaped data. PII masked automatically. Zero risk to production.
```
┌──────────────────────────────────────────────────────┐
│  MAIN DB (read only — never written to by pgDelta)   │
│  Your real tables, your real data, untouched         │
└─────────────────────┬────────────────────────────────┘
                      │ SELECT only (read replica safe)
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
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_macOS_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### macOS — Intel
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_macOS_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — amd64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_Linux_x86_64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux — arm64
```bash
curl -sSL https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_Linux_arm64.tar.gz | tar xz
sudo mv pgdelta /usr/local/bin/
pgdelta --help
```

### Linux packages
```bash
# Debian / Ubuntu
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_linux_amd64.deb
sudo dpkg -i pgdelta_1.0.2_linux_amd64.deb

# RHEL / Fedora / CentOS
wget https://github.com/ankeshk7/pgdelta/releases/latest/download/pgdelta_1.0.2_linux_amd64.rpm
sudo rpm -i pgdelta_1.0.2_linux_amd64.rpm
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
```sql
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
  Connecting to main DB...      ✓  (PostgreSQL 16.x)
  Connecting to branch DB...    ✓  (PostgreSQL 16.x)
  Creating pgdelta schema...    ✓
  Creating branch_main...       ✓
  Scanning for PII columns...   ✓  (2 sensitive columns detected)
    → users.email
    → users.phone
  Writing .pgdelta.yml...       ✓
  Installing git hooks...       ✓
```

### 2. Auto-Configure Snapshot Queries

pgDelta scans your schema and AI generates the right extraction query for each table. Tables are detected automatically — no hardcoding needed.
```bash
pgdelta configure
```
```
  Found 5 tables: users, orders, products, payments, audit_logs

  Asking AI to suggest extraction queries...  ✓

  users      SELECT * FROM users WHERE status = 'active' LIMIT 5000
  orders     SELECT * FROM orders WHERE created_at >= NOW() - INTERVAL '30 days' LIMIT 10000
  products   SELECT * FROM products LIMIT 5000
  payments   SELECT * FROM payments WHERE status = 'completed' LIMIT 5000
  audit_logs SELECT * FROM audit_logs LIMIT 1000

  Write these queries to .pgdelta.yml? [Y/n]: Y
  ✓  .pgdelta.yml updated.
```

Run again anytime to regenerate. Add a single new table without touching others:
```bash
pgdelta configure --table=new_payments_table
```

### 3. Configure PII Masking

pgDelta automatically detects sensitive columns on init. Add masking rules to `.pgdelta.yml`:
```yaml
pii:
  auto_detect: true
  masks:
    users:
      email: "masked_{{id}}@example.com"   # unique per row
      phone: "+10000000000"
      name: "REDACTED"
      ssn: "***-**-{{last4}}"              # keeps last 4 digits
```

Supported patterns:
- `{{id}}` — replaced with the row's id (ensures uniqueness)
- `{{last4}}` — replaced with last 4 characters of the original value
- Any static string — e.g. `"REDACTED"`, `"+10000000000"`

Masks are enforced automatically on every snapshot. Real data never leaves your main DB.

### 4. Verify Setup
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

### 5. Commit Config and Work Normally
```bash
git add .pgdelta.yml
git commit -m "chore: add pgDelta config"

# Now just use git — pgDelta handles everything else
git checkout -b feature-payments
# → branch created automatically
# → tables loaded with real data, PII masked
# → completely isolated from teammates
```

---

## The Complete Developer Workflow
```bash
# Day 1 setup (5 minutes)
pgdelta init
pgdelta configure
pgdelta protect add main
git add .pgdelta.yml && git commit -m "chore: add pgDelta"

# Daily workflow (zero extra effort)
git checkout -b feature-stripe

# → branch created automatically from parent schema
# → users, orders, products loaded in parallel
# → emails masked, names REDACTED, real data shape preserved
# → nobody else affected

pgdelta migrate "ALTER TABLE users ADD COLUMN stripe_id TEXT"
# AI: ✓ safe — nullable column, no data impact

pgdelta migrate "CREATE INDEX idx_stripe ON users(stripe_id)"
# AI: ✓ safe

# Test against real production-shaped data
# Ship with confidence

git merge feature-stripe
# → ALTER TABLE applied to parent
# → CREATE INDEX applied to parent
# → Test data discarded
# → Branch schema dropped, storage freed
```

---

## Key Design Decisions

### Tables Detected Automatically

pgDelta reads your actual schema. `pgdelta configure` detects every table and AI generates appropriate queries. Run again when you add new tables.

### PII Masked On Extraction

Sensitive data is masked before it ever enters the branch DB using SQL expression rewriting:
```
Original query:  SELECT * FROM users WHERE status = 'active'
Executed query:  SELECT id,
                   'masked_' || id::text || '@example.com' AS email,
                   'REDACTED' AS name,
                   status, plan, created_at
                 FROM users WHERE status = 'active'
```

Real emails never stored. Real names never stored. Branch has realistic data shape with safe values.

### Migrations Only On Merge

Only schema changes (DDL) travel to the parent on merge. Test data is discarded. Main DB untouched.
```sql
-- Travels to parent:
ALTER TABLE users ADD COLUMN stripe_id TEXT;
CREATE INDEX idx_stripe ON users(stripe_id);

-- Discarded on merge:
INSERT INTO users VALUES ('test@test.com', ...);

-- Mark as seed to travel with DDL:
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
  schema_from: git-parent
  auto_create: true
  auto_cleanup: true
  auto_merge: true

# Snapshot queries — auto-generated by pgdelta configure
# Run 'pgdelta configure' to regenerate
# Add a single table: pgdelta configure --table=<name>
snapshots:
  default_row_limit: 5000
  chunk_size: 10000
  parallel_tables: 3
  resume_on_interrupt: true
  tables:
    users:
      query: "SELECT * FROM users WHERE status = 'active'"
    orders:
      query: "SELECT * FROM orders WHERE created_at > now() - interval '30 days'"
    products:
      query: "SELECT * FROM products"
  exclude:
    - audit_logs
    - sessions
    - password_reset_tokens

# PII masking — enforced automatically on every snapshot
# Patterns: {{id}} = row id, {{last4}} = last 4 chars, or static string
pii:
  auto_detect: true
  masks:
    users:
      email: "masked_{{id}}@example.com"
      phone: "+10000000000"
      name: "REDACTED"
      ssn: "***-**-{{last4}}"

# AI — bring your own Anthropic API key
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

# Anonymous telemetry — helps improve pgDelta
telemetry:
  enabled: true
  anonymous: true
```

---

## All Commands
```bash
# ── SETUP ──────────────────────────────────────────────────────
pgdelta init                           # initialize pgDelta in repo
pgdelta configure                      # AI auto-generate snapshot config
pgdelta configure --table=<name>       # add/update single table config
pgdelta configure --no-ai              # use simple SELECT * queries
pgdelta doctor [--fix]                 # 10 health checks + auto-fix
pgdelta team init                      # generate team-ready config
pgdelta team status                    # team branch activity overview

# ── BRANCHING ──────────────────────────────────────────────────
pgdelta create <branch>                # create DB branch
pgdelta clone <source> <new>           # clone branch with data + migrations
pgdelta switch <branch>                # switch DB context
pgdelta delete <branch>                # drop branch + free storage
pgdelta delete <branch> --force        # skip unmerged migration warning
pgdelta list                           # list active branches
pgdelta list --all                     # include merged + deleted
pgdelta status                         # current branch + divergence warning
pgdelta diff <b1> [b2]                 # schema diff between branches
pgdelta diff <b1> [b2] --migrations    # migration diff instead of schema

# ── DATA ───────────────────────────────────────────────────────
pgdelta snapshot <table>               # snapshot table (AI suggests query)
pgdelta snapshot <table> --no-ai       # skip AI suggestion
pgdelta snapshot --all                 # snapshot all configured tables
pgdelta url [branch]                   # connection string for branch
pgdelta url [branch] --format=env      # as DATABASE_URL=...
pgdelta url [branch] --format=json     # as JSON

# ── MIGRATIONS ─────────────────────────────────────────────────
pgdelta migrate "<sql>"                # apply migration + AI risk check
pgdelta migrate "<sql>" --no-ai        # skip risk analysis
pgdelta migrate "<sql>" --type=seed    # mark as seed data
pgdelta log [branch]                   # migration history (git log style)
pgdelta log [branch] --type=ddl        # filter by type
pgdelta reset [branch]                 # undo last migration
pgdelta reset [branch] --steps=3       # undo last 3 migrations
pgdelta reset [branch] --force         # skip confirmation
pgdelta merge <branch>                 # merge DDL to parent
pgdelta merge <branch> --dry-run       # simulate only
pgdelta merge <branch> --strategy=theirs   # auto-resolve conflicts
pgdelta rebase <branch>                # replay on parent's latest
pgdelta rebase <branch> --onto main    # rebase onto specific branch

# ── STASH & TAG ────────────────────────────────────────────────
pgdelta stash save [message]           # stash current migration state
pgdelta stash pop                      # restore last stash
pgdelta stash list                     # list all stashes
pgdelta stash drop [id]                # drop a stash
pgdelta tag create <name> [message]    # create named checkpoint
pgdelta tag list                       # list all tags
pgdelta tag delete <name>              # delete a tag

# ── PROTECT ────────────────────────────────────────────────────
pgdelta protect add <branch>           # protect from direct merge
pgdelta protect remove <branch>        # remove protection
pgdelta protect list                   # list protected branches

# ── AUDIT ──────────────────────────────────────────────────────
pgdelta audit                          # export all activity (table format)
pgdelta audit --format=csv             # CSV export
pgdelta audit --format=json            # JSON export
pgdelta audit --out=audit.csv          # write to file
pgdelta audit --branch=main            # filter by branch

# ── CI/CD ──────────────────────────────────────────────────────
pgdelta ci github                      # generate GitHub Actions workflow
pgdelta ci github --test-cmd "make test"

# ── DASHBOARD ──────────────────────────────────────────────────
pgdelta dashboard                      # open web dashboard
pgdelta dashboard --port=8080          # custom port
pgdelta dashboard --no-open            # don't auto-open browser
```

---

## AI Features
```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

**Schema configuration** — detects all tables, generates appropriate queries:
```bash
pgdelta configure
# → scans every table in your DB
# → AI generates right query per table context
# → saved to .pgdelta.yml for team consistency
```

**Extraction suggestions** — AI analyzes branch name + schema:
```
Branch: feature-payments
Table:  users

AI suggests:
SELECT * FROM users WHERE has_payment_method = true LIMIT 5000
```

**Migration risk detection** — warns before dangerous DDL:
```
pgdelta migrate "ALTER TABLE users DROP COLUMN email"

⚠ WARNING: Dropping email is risky.
  Likely used for authentication — could break login.
  Found in codebase: src/auth/login.go:45

Proceed anyway? [y/N]:
```

**PII auto-detection** — scans schema on init, flags sensitive columns automatically.

**Enterprise:** Bring your own API key — data never sent to third parties.

---

## PII Masking

pgDelta enforces PII masking at extraction time. Sensitive data is rewritten in SQL before it's streamed to the branch DB.

### How It Works
```
Config:
  users.email: "masked_{{id}}@example.com"
  users.name:  "REDACTED"

Main DB:                    Branch DB:
alice@example.com    →      masked_1@example.com
Bob                  →      REDACTED
charlie@example.com  →      masked_3@example.com
Charlie              →      REDACTED
```

### Mask Patterns
```yaml
pii:
  masks:
    users:
      email: "masked_{{id}}@example.com"  # unique per row using id
      name: "REDACTED"                     # static replacement
      phone: "+10000000000"                # static replacement
      ssn: "***-**-{{last4}}"             # keeps last 4 digits
      ip_address: "0.0.0.0"               # static replacement
```

### Auto-Detection

On `pgdelta init`, pgDelta scans your schema and flags columns that likely contain PII:
- `email`, `mail` → email pattern
- `phone`, `mobile`, `tel` → phone pattern
- `ssn`, `national_id` → SSN pattern
- `name`, `first_name`, `last_name` → name pattern
- `address`, `street`, `zip` → address pattern
- `ip`, `ip_address` → IP pattern
- `card_number`, `pan` → card pattern

---

## Team Setup
```bash
# One-time team setup (5 minutes)
pgdelta init
pgdelta configure
pgdelta protect add main
pgdelta ci github

git add .pgdelta.yml .github/workflows/pgdelta.yml
git commit -m "chore: add pgDelta team config"
git push
```

Each team member then:
```bash
git clone your-repo
export MAIN_DATABASE_URL="..."
export BRANCH_DATABASE_URL="..."
pgdelta init
# Done — hooks installed, ready to work
```

Every PR automatically gets its own isolated database branch via GitHub Actions.

---

## GitHub Actions Integration
```bash
pgdelta ci github --test-cmd "go test ./..."
# writes .github/workflows/pgdelta.yml
```

Add to your GitHub repo secrets:
```
MAIN_DATABASE_URL
BRANCH_DATABASE_URL
ANTHROPIC_API_KEY   (optional)
```

Every PR gets:
- Isolated database branch with real data
- PII masked automatically
- Tests run against branch `DATABASE_URL`
- Branch cleaned up when PR closes

---

## Web Dashboard
```bash
pgdelta dashboard
# Opens http://localhost:7433
```

GitHub-style interface showing all branches, migrations, snapshots — live, auto-refreshing.

---

## Audit Log
```bash
pgdelta audit
pgdelta audit --format=csv --out=audit-march.csv
pgdelta audit --format=json
pgdelta audit --branch=main
```

Captures every branch creation, migration, snapshot, and merge. Export for compliance.

---

## Data Safety

| Guarantee | How |
|---|---|
| Main DB never written to | Read-only connection + dedicated DB user |
| PII masked automatically | SQL expression rewriting at extraction time |
| Unique masked values | `{{id}}` pattern ensures no unique constraint violations |
| Data never leaves your infra | Self-hosted, no cloud dependency |
| AI key stays with you | Bring your own Anthropic key |
| Full audit trail | Every action logged |
| Protected branches | Prevent accidental merges to production |
| Resumable snapshots | Ctrl+C safe — picks up where stopped |

---

## Why Not...

**Neon?** Cloud-only. Your data never leaves with pgDelta.

**PlanetScale?** MySQL only. Schema-only branching — no data.

**Dolt?** Rewrites Postgres storage engine. Slow and unfamiliar.

**Shared dev DB?** You already know why not.

---

## Architecture
```
Developer (git commands only)
    ↓ git hooks fire automatically
pgDelta CLI (Go — single binary, no runtime)
    ├── Branch Engine      create, clone, switch, delete, diff, log
    ├── Snapshot Engine    lazy load via COPY + keyset pagination + PII masking
    ├── Migration Engine   capture, reset, merge, rebase, stash, tag
    ├── Conflict Engine    simulate before apply, never blind merge
    ├── AI Engine          Claude API — configure, suggestions, risk
    ├── PII Engine         SQL expression rewriting, auto-detection
    ├── Protect Engine     branch protection + git hook enforcement
    ├── Audit Engine       CSV/JSON activity log export
    ├── Team Engine        shared config generation
    └── Dashboard          GitHub-style web UI, live refresh
    ↓
Main DB (read only)         Branch DB (pgDelta owned)
Your prod replica           One Postgres schema per branch
```

**Tech stack:** Go · Cobra · pgx v5 · SQLite · Goreleaser · Claude API

---

## Roadmap
```
v1.0  ✓  Complete — branching, snapshots, migrations, merge, rebase,
          AI, dashboard, clone, diff, log, reset, stash, tag,
          protect, audit, team, PII masking
v1.1  →  Enterprise SSO + compliance report exports
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

---

## License

MIT — see [LICENSE](LICENSE)

---

Built by **Ankesh Kedia** — Senior Software Engineer.

---

<p align="center">
  <strong>pgDelta Δ — v1.0.2</strong><br><br>
  <a href="https://github.com/ankeshk7/pgdelta/releases">Releases</a> ·
  <a href="https://github.com/ankeshk7/pgdelta/issues">Issues</a> ·
  <a href="https://github.com/ankeshk7/pgdelta">GitHub</a>
</p>
