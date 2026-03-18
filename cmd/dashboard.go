package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/ankeshkedia/pgdelta/internal/config"
	"github.com/ankeshkedia/pgdelta/internal/db"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start the pgDelta web dashboard",
	RunE:  runDashboard,
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
	dashboardCmd.Flags().Int("port", 7433, "Port to run dashboard on")
	dashboardCmd.Flags().Bool("no-open", false, "Don't open browser automatically")
}

type BranchInfo struct {
	Name           string     `json:"name"`
	ParentBranch   string     `json:"parent_branch"`
	SchemaName     string     `json:"schema_name"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
	MigrationCount int        `json:"migration_count"`
	SnapshotCount  int        `json:"snapshot_count"`
}

type MigrationInfo struct {
	Sequence    int       `json:"sequence"`
	SQL         string    `json:"sql"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Checksum    string    `json:"checksum"`
	AppliedAt   time.Time `json:"applied_at"`
}

type SnapshotInfo struct {
	TableName     string     `json:"table_name"`
	Status        string     `json:"status"`
	RowsLoaded    int64      `json:"rows_loaded"`
	RowCount      int64      `json:"row_count"`
	ExtractionSQL string     `json:"extraction_sql"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type DashboardData struct {
	Branches       []BranchInfo `json:"branches"`
	TotalBranches  int          `json:"total_branches"`
	ActiveBranches int          `json:"active_branches"`
	MergedBranches int          `json:"merged_branches"`
	Version        string       `json:"version"`
}

func runDashboard(cmd *cobra.Command, args []string) error {
	port, _ := cmd.Flags().GetInt("port")
	noOpen, _ := cmd.Flags().GetBool("no-open")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("pgDelta not initialized — run 'pgdelta init' first")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/branches/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		branchName := r.URL.Path[len("/api/branches/"):]
		for i, c := range branchName {
			if c == '/' {
				branchName = branchName[:i]
				break
			}
		}
		if branchName == "" {
			http.Error(w, "branch name required", 400)
			return
		}

		dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer dbManager.Close()

		var branchID string
		err = dbManager.Branch.QueryRow(ctx,
			"SELECT id FROM pgdelta.branches WHERE name = $1", branchName,
		).Scan(&branchID)
		if err != nil {
			http.Error(w, "branch not found", 404)
			return
		}

		migRows, err := dbManager.Branch.Query(ctx, `
			SELECT sequence, sql, type, COALESCE(description,''), checksum, applied_at
			FROM pgdelta.branch_migrations WHERE branch_id = $1 ORDER BY sequence ASC
		`, branchID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer migRows.Close()

		var migrations []MigrationInfo
		for migRows.Next() {
			var m MigrationInfo
			if err := migRows.Scan(&m.Sequence, &m.SQL, &m.Type, &m.Description, &m.Checksum, &m.AppliedAt); err != nil {
				continue
			}
			migrations = append(migrations, m)
		}

		snapRows, err := dbManager.Branch.Query(ctx, `
			SELECT table_name, status, rows_loaded, row_count, extraction_sql, completed_at
			FROM pgdelta.branch_data_snapshots WHERE branch_id = $1 ORDER BY table_name
		`, branchID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer snapRows.Close()

		var snapshots []SnapshotInfo
		for snapRows.Next() {
			var s SnapshotInfo
			if err := snapRows.Scan(&s.TableName, &s.Status, &s.RowsLoaded, &s.RowCount, &s.ExtractionSQL, &s.CompletedAt); err != nil {
				continue
			}
			snapshots = append(snapshots, s)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"branch":     branchName,
			"migrations": migrations,
			"snapshots":  snapshots,
		})
	})

	mux.HandleFunc("/api/branches", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		dbManager, err := db.New(ctx, cfg.MainDB.URL, cfg.BranchDB.URL)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer dbManager.Close()

		rows, err := dbManager.Branch.Query(ctx, `
			SELECT b.name, b.parent_branch, b.schema_name, b.status,
				b.created_at, b.merged_at,
				COUNT(DISTINCT m.id), COUNT(DISTINCT s.id)
			FROM pgdelta.branches b
			LEFT JOIN pgdelta.branch_migrations m ON m.branch_id = b.id
			LEFT JOIN pgdelta.branch_data_snapshots s ON s.branch_id = b.id AND s.status = 'ready'
			WHERE b.status != 'deleted'
			GROUP BY b.id, b.name, b.parent_branch, b.schema_name, b.status, b.created_at, b.merged_at
			ORDER BY b.created_at DESC
		`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		var branches []BranchInfo
		total, active, merged := 0, 0, 0
		for rows.Next() {
			var b BranchInfo
			if err := rows.Scan(&b.Name, &b.ParentBranch, &b.SchemaName, &b.Status,
				&b.CreatedAt, &b.MergedAt, &b.MigrationCount, &b.SnapshotCount); err != nil {
				continue
			}
			branches = append(branches, b)
			total++
			if b.Status == "active" {
				active++
			} else if b.Status == "merged" {
				merged++
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(DashboardData{
			Branches:       branches,
			TotalBranches:  total,
			ActiveBranches: active,
			MergedBranches: merged,
			Version:        "0.6.0",
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(getDashboardHTML()))
	})

	addr := fmt.Sprintf(":%d", port)
	url := fmt.Sprintf("http://localhost:%d", port)

	fmt.Println()
	fmt.Println("  pgDelta — dashboard")
	fmt.Println("  ─────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Dashboard running at: %s\n", url)
	fmt.Println("  Press Ctrl+C to stop")
	fmt.Println()

	if !noOpen {
		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(url)
		}()
	}

	return http.ListenAndServe(addr, mux)
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "linux":
		exec.Command("xdg-open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
}

func getDashboardHTML() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>pgDelta Dashboard</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{
  --bg:#ffffff;--bg2:#f6f8fa;--bg3:#eef1f4;
  --border:#d0d7de;--border2:#b9c0c9;
  --text:#1f2328;--text2:#656d76;--text3:#9ba3ad;
  --blue:#0969da;--blue-bg:#dbeafe;--blue-border:#bfdbfe;
  --green:#1a7f37;--green-bg:#dafbe1;--green-border:#aef2bb;
  --orange:#9a6700;--orange-bg:#fff8c5;
  --red:#cf222e;--red-bg:#ffebe9;
  --purple:#8250df;--purple-bg:#fbefff;
  --shadow:0 1px 3px rgba(31,35,40,0.12),0 1px 0 rgba(31,35,40,0.04);
}
body{font-family:"Inter",-apple-system,sans-serif;font-size:14px;line-height:1.5;color:var(--text);background:var(--bg);-webkit-font-smoothing:antialiased}

/* HEADER */
.hdr{height:56px;border-bottom:1px solid var(--border);padding:0 24px;display:flex;align-items:center;gap:16px;background:var(--bg);position:sticky;top:0;z-index:100}
.logo{display:flex;align-items:center;gap:8px}
.logo-mark{width:30px;height:30px;background:var(--text);border-radius:7px;display:flex;align-items:center;justify-content:center;color:#fff;font-weight:700;font-size:13px;font-family:"JetBrains Mono",monospace;letter-spacing:-1px}
.logo-name{font-weight:700;font-size:15px;color:var(--text)}
.hdr-sep{width:1px;height:18px;background:var(--border)}
.hdr-nav{display:flex;gap:2px}
.nav-btn{padding:5px 10px;border-radius:6px;font-size:13px;font-weight:500;color:var(--text2);cursor:pointer;border:none;background:none;transition:all .15s}
.nav-btn:hover,.nav-btn.on{background:var(--bg2);color:var(--text)}
.hdr-right{margin-left:auto;display:flex;align-items:center;gap:12px}
.live-pill{display:flex;align-items:center;gap:5px;padding:3px 10px;border-radius:20px;background:var(--green-bg);border:1px solid var(--green-border);font-size:12px;font-weight:600;color:var(--green)}
.live-dot{width:6px;height:6px;background:var(--green);border-radius:50%;animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:.6;transform:scale(1.2)}}
.upd{font-size:11px;color:var(--text3);font-family:"JetBrains Mono",monospace}

/* PAGE */
.page{max-width:1280px;margin:0 auto;padding:28px 24px}
.page-hdr{display:flex;align-items:flex-start;justify-content:space-between;margin-bottom:24px}
.page-title{font-size:20px;font-weight:600;color:var(--text);margin-bottom:2px}
.page-sub{font-size:13px;color:var(--text2)}

/* STATS */
.stats{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:24px}
.sc{background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:16px 20px;box-shadow:var(--shadow)}
.sc-label{font-size:12px;font-weight:500;color:var(--text2);margin-bottom:8px;display:flex;align-items:center;gap:5px}
.sc-value{font-size:28px;font-weight:700;font-family:"JetBrains Mono",monospace;color:var(--text);line-height:1}
.sc-value.blue{color:var(--blue)}
.sc-value.green{color:var(--green)}

/* LAYOUT */
.layout{display:grid;grid-template-columns:1fr 360px;gap:16px;align-items:start}

/* PANEL */
.panel{background:var(--bg);border:1px solid var(--border);border-radius:8px;box-shadow:var(--shadow);overflow:hidden}
.ph{padding:12px 16px;border-bottom:1px solid var(--border);background:var(--bg2);display:flex;align-items:center;gap:8px}
.ph-title{font-size:13px;font-weight:600;color:var(--text)}
.ph-count{margin-left:auto;font-size:11px;font-weight:600;padding:1px 7px;border-radius:20px;background:var(--bg3);color:var(--text2);border:1px solid var(--border);font-family:"JetBrains Mono",monospace}

/* SEARCH & FILTER */
.search-wrap{padding:10px 16px;border-bottom:1px solid var(--border)}
.search-input{width:100%;padding:6px 10px 6px 32px;border:1px solid var(--border);border-radius:6px;font-size:13px;font-family:inherit;background:var(--bg);color:var(--text);outline:none;background-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='14' height='14' viewBox='0 0 24 24' fill='none' stroke='%239ba3ad' stroke-width='2'%3E%3Ccircle cx='11' cy='11' r='8'/%3E%3Cpath d='m21 21-4.35-4.35'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:10px center;transition:border-color .15s,box-shadow .15s}
.search-input:focus{border-color:var(--blue);box-shadow:0 0 0 3px rgba(9,105,218,.1)}
.filter-bar{padding:8px 16px;border-bottom:1px solid var(--border);display:flex;gap:4px}
.fb{padding:4px 10px;border-radius:6px;font-size:12px;font-weight:500;color:var(--text2);cursor:pointer;border:1px solid transparent;background:none;transition:all .15s}
.fb:hover{background:var(--bg2);border-color:var(--border);color:var(--text)}
.fb.on{background:var(--bg);border-color:var(--border);color:var(--text);font-weight:600}

/* BRANCH LIST */
.branch-list{overflow-y:auto;max-height:620px}
.br{display:flex;align-items:center;padding:12px 16px;border-bottom:1px solid var(--border);cursor:pointer;transition:background .1s;gap:12px}
.br:hover{background:var(--bg2)}
.br.sel{background:var(--blue-bg)}
.br:last-child{border-bottom:none}
.bi{width:34px;height:34px;border-radius:7px;border:1px solid var(--border);display:flex;align-items:center;justify-content:center;font-size:15px;flex-shrink:0;background:var(--bg2);color:var(--text2);font-family:"JetBrains Mono",monospace}
.bi.act{background:var(--green-bg);border-color:var(--green-border);color:var(--green)}
.bi.mrg{background:var(--blue-bg);border-color:var(--blue-border);color:var(--blue)}
.binfo{flex:1;min-width:0}
.bname{font-size:13px;font-weight:600;color:var(--text);font-family:"JetBrains Mono",monospace;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;margin-bottom:3px}
.bmeta{display:flex;gap:12px;font-size:11px;color:var(--text2)}
.bright{display:flex;flex-direction:column;align-items:flex-end;gap:4px;flex-shrink:0}
.badge{display:inline-flex;align-items:center;padding:2px 8px;border-radius:20px;font-size:11px;font-weight:600}
.badge.act{background:var(--green-bg);color:var(--green);border:1px solid var(--green-border)}
.badge.mrg{background:var(--blue-bg);color:var(--blue);border:1px solid var(--blue-border)}
.badge.del{background:var(--bg2);color:var(--text3);border:1px solid var(--border)}
.bage{font-size:11px;color:var(--text3);font-family:"JetBrains Mono",monospace}

/* DETAIL */
.dp{display:flex;flex-direction:column;gap:12px}
.empty{padding:40px 24px;text-align:center;color:var(--text3);font-size:13px;line-height:1.8}
.empty-icon{font-size:28px;margin-bottom:8px;display:block}

/* MIGRATION ROW */
.mr{padding:10px 16px;border-bottom:1px solid var(--border);display:flex;align-items:flex-start;gap:10px}
.mr:last-child{border-bottom:none}
.mnum{font-size:11px;font-family:"JetBrains Mono",monospace;color:var(--text3);min-width:28px;padding-top:2px}
.mcontent{flex:1;min-width:0}
.mdesc{font-size:13px;font-weight:500;color:var(--text);margin-bottom:2px}
.msql{font-size:11px;font-family:"JetBrains Mono",monospace;color:var(--text3);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.mbadge{font-size:10px;font-weight:700;padding:2px 7px;border-radius:4px;flex-shrink:0;letter-spacing:.3px}
.mbadge.ddl{background:var(--blue-bg);color:var(--blue)}
.mbadge.seed{background:var(--green-bg);color:var(--green)}
.mbadge.test{background:var(--bg2);color:var(--text2);border:1px solid var(--border)}

/* SNAPSHOT ROW */
.sr{padding:10px 16px;border-bottom:1px solid var(--border);display:flex;align-items:center;gap:10px}
.sr:last-child{border-bottom:none}
.sdot{width:8px;height:8px;border-radius:50%;flex-shrink:0}
.sdot.ready{background:var(--green)}
.sdot.loading{background:var(--orange);animation:pulse 1s infinite}
.sdot.failed{background:var(--red)}
.sdot.pending{background:var(--text3)}
.sinfo{flex:1}
.stable{font-size:13px;font-weight:500;color:var(--text);font-family:"JetBrains Mono",monospace}
.srows{font-size:11px;color:var(--text2);margin-top:1px}
.sbadge{font-size:11px;font-weight:600;padding:2px 8px;border-radius:20px}
.sbadge.ready{background:var(--green-bg);color:var(--green)}
.sbadge.failed{background:var(--red-bg);color:var(--red)}
.sbadge.loading{background:var(--orange-bg);color:var(--orange)}
.sbadge.pending{background:var(--bg2);color:var(--text2)}

/* SKELETON */
.skel{background:linear-gradient(90deg,var(--bg2) 25%,var(--bg3) 50%,var(--bg2) 75%);background-size:200% 100%;animation:shimmer 1.5s infinite;border-radius:4px;height:13px;margin:4px 0}
@keyframes shimmer{0%{background-position:200% 0}100%{background-position:-200% 0}}

::-webkit-scrollbar{width:5px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--border);border-radius:3px}
</style>
</head>
<body>

<div class="hdr">
  <div class="logo">
    <div class="logo-mark">pg&#x394;</div>
    <span class="logo-name">pgDelta</span>
  </div>
  <div class="hdr-sep"></div>
  <nav class="hdr-nav">
    <button class="nav-btn on">Branches</button>
    <button class="nav-btn" onclick="window.open('https://github.com/ankeshk7/pgdelta','_blank')">Docs</button>
  </nav>
  <div class="hdr-right">
    <span class="upd" id="upd">Loading...</span>
    <div class="live-pill"><div class="live-dot"></div>Live</div>
  </div>
</div>

<div class="page">
  <div class="page-hdr">
    <div>
      <div class="page-title">Database Branches</div>
      <div class="page-sub">All branches across your Postgres instance</div>
    </div>
  </div>

  <div class="stats">
    <div class="sc">
      <div class="sc-label">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 01-9 9"/></svg>
        Total Branches
      </div>
      <div class="sc-value blue" id="sTotal">-</div>
    </div>
    <div class="sc">
      <div class="sc-label">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg>
        Active
      </div>
      <div class="sc-value green" id="sActive">-</div>
    </div>
    <div class="sc">
      <div class="sc-label">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M13 6h3a2 2 0 012 2v7"/><line x1="6" y1="9" x2="6" y2="21"/></svg>
        Merged
      </div>
      <div class="sc-value" id="sMerged">-</div>
    </div>
    <div class="sc">
      <div class="sc-label">
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/></svg>
        Version
      </div>
      <div class="sc-value" id="sVersion" style="font-size:18px;margin-top:4px">-</div>
    </div>
  </div>

  <div class="layout">
    <div class="panel">
      <div class="ph">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 01-9 9"/></svg>
        <span class="ph-title">Branches</span>
        <span class="ph-count" id="bCount">0</span>
      </div>
      <div class="search-wrap">
        <input class="search-input" id="searchInput" type="text"
          placeholder="Search branches..." oninput="doFilter()">
      </div>
      <div class="filter-bar">
        <button class="fb on" onclick="setF('all',this)">All</button>
        <button class="fb" onclick="setF('active',this)">Active</button>
        <button class="fb" onclick="setF('merged',this)">Merged</button>
      </div>
      <div class="branch-list" id="bList">
        <div class="empty"><div class="skel" style="width:60%;margin:0 auto 8px"></div><div class="skel" style="width:40%;margin:0 auto"></div></div>
      </div>
    </div>

    <div class="dp" id="dp">
      <div class="panel">
        <div class="empty">
          <span class="empty-icon">&#x2442;</span>
          Select a branch to view<br>migrations and snapshots
        </div>
      </div>
    </div>
  </div>
</div>

<script>
var allData=null,selBranch=null,curFilter='all',searchQ='';

function ago(d){
  var s=Math.floor((new Date()-new Date(d))/1000);
  if(s<60)return'just now';
  if(s<3600)return Math.floor(s/60)+'m ago';
  if(s<86400)return Math.floor(s/3600)+'h ago';
  return Math.floor(s/86400)+'d ago';
}
function tr(s,n){return s&&s.length>n?s.slice(0,n)+'...':s||''}

function setF(f,btn){
  curFilter=f;
  document.querySelectorAll('.fb').forEach(function(b){b.classList.remove('on')});
  btn.classList.add('on');
  renderBranches();
}
function doFilter(){searchQ=document.getElementById('searchInput').value.toLowerCase();renderBranches();}

function renderBranches(){
  if(!allData)return;
  var list=document.getElementById('bList');
  var branches=allData.branches||[];
  if(curFilter!=='all')branches=branches.filter(function(b){return b.status===curFilter;});
  if(searchQ)branches=branches.filter(function(b){return b.name.toLowerCase().indexOf(searchQ)>=0||b.parent_branch.toLowerCase().indexOf(searchQ)>=0;});
  document.getElementById('bCount').textContent=branches.length;
  if(branches.length===0){list.innerHTML='<div class="empty">No branches match your filter</div>';return;}
  list.innerHTML=branches.map(function(b){
    var iCls=b.status==='active'?'act':b.status==='merged'?'mrg':'';
    var icon=b.status==='active'?'&#x2442;':b.status==='merged'?'&#x2713;':'&#x2715;';
    var bCls='badge '+(b.status==='active'?'act':b.status==='merged'?'mrg':'del');
    var sel=selBranch===b.name?' sel':'';
    return '<div class="br'+sel+'" onclick="selB(\''+b.name+'\')">'
      +'<div class="bi '+iCls+'">'+icon+'</div>'
      +'<div class="binfo">'
        +'<div class="bname">'+b.name+'</div>'
        +'<div class="bmeta">'
          +'<span>from '+b.parent_branch+'</span>'
          +'<span>'+b.migration_count+' migrations</span>'
          +'<span>'+b.snapshot_count+' snapshots</span>'
        +'</div>'
      +'</div>'
      +'<div class="bright">'
        +'<span class="'+bCls+'">'+b.status+'</span>'
        +'<span class="bage">'+ago(b.created_at)+'</span>'
      +'</div>'
    +'</div>';
  }).join('');
}

function selB(name){
  selBranch=name;
  renderBranches();
  var dp=document.getElementById('dp');
  dp.innerHTML='<div class="panel"><div class="empty"><div class="skel" style="width:70%;margin:0 auto 8px"></div><div class="skel" style="width:50%;margin:0 auto"></div></div></div>';
  fetch('/api/branches/'+encodeURIComponent(name))
    .then(function(r){return r.json();})
    .then(function(d){
      var migs=d.migrations||[];
      var snaps=d.snapshots||[];
      var mHTML=migs.length===0
        ?'<div class="empty">No migrations yet</div>'
        :migs.map(function(m){
            return '<div class="mr">'
              +'<span class="mnum">#'+m.sequence+'</span>'
              +'<div class="mcontent">'
                +'<div class="mdesc">'+tr(m.description||'Migration',50)+'</div>'
                +'<div class="msql">'+tr(m.sql,70)+'</div>'
              +'</div>'
              +'<span class="mbadge '+m.type+'">'+m.type.toUpperCase()+'</span>'
            +'</div>';
          }).join('');
      var sHTML=snaps.length===0
        ?'<div class="empty">No snapshots yet</div>'
        :snaps.map(function(s){
            return '<div class="sr">'
              +'<div class="sdot '+s.status+'"></div>'
              +'<div class="sinfo">'
                +'<div class="stable">'+s.table_name+'</div>'
                +'<div class="srows">'+s.rows_loaded.toLocaleString()+' rows loaded</div>'
              +'</div>'
              +'<span class="sbadge '+s.status+'">'+s.status+'</span>'
            +'</div>';
          }).join('');
      dp.innerHTML=''
        +'<div class="panel">'
          +'<div class="ph">'
            +'<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/></svg>'
            +'<span class="ph-title">Migrations</span>'
            +'<span class="ph-count">'+migs.length+'</span>'
          +'</div>'
          +mHTML
        +'</div>'
        +'<div class="panel">'
          +'<div class="ph">'
            +'<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3"/><path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5"/></svg>'
            +'<span class="ph-title">Snapshots</span>'
            +'<span class="ph-count">'+snaps.length+'</span>'
          +'</div>'
          +sHTML
        +'</div>';
    })
    .catch(function(){dp.innerHTML='<div class="panel"><div class="empty">Failed to load details</div></div>';});
}

function refresh(){
  fetch('/api/branches')
    .then(function(r){return r.json();})
    .then(function(d){
      allData=d;
      document.getElementById('sTotal').textContent=d.total_branches||0;
      document.getElementById('sActive').textContent=d.active_branches||0;
      document.getElementById('sMerged').textContent=d.merged_branches||0;
      document.getElementById('sVersion').textContent='v'+(d.version||'-');
      renderBranches();
      document.getElementById('upd').textContent='Updated '+new Date().toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'});
    })
    .catch(function(){document.getElementById('upd').textContent='Connection error';});
}

refresh();
setInterval(refresh,5000);
</script>
</body>
</html>`
}
