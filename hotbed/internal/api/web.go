package api

// Embedded dashboard: one page, vanilla JS + SSE, no build step.

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Hotbed - OpenCode Sandboxes</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Crect width='64' height='64' rx='14' fill='%230d1220'/%3E%3Crect x='9' y='13' width='46' height='38' rx='5' fill='none' stroke='%234cc38a' stroke-width='4'/%3E%3Cpath d='M20 33h24M20 40h16' stroke='%238d99ab' stroke-width='3' stroke-linecap='round'/%3E%3C/svg%3E">
<link rel="stylesheet" href="/static/style.css">
<script>document.documentElement.setAttribute('data-theme',localStorage.getItem('hotbed-theme')||'dark');</script>
</head>
<body>
<header id="topbar">
  <div id="brand" title="Overview - host &amp; sandboxes"><span class="logo">HBD</span><span class="brandname">Desktop</span><span id="ver"></span></div>
  <div id="health">
    <span class="hbadge" id="h-docker">docker</span>
    <span class="hbadge" id="h-libvirt">libvirt</span>
    <span class="hbadge" id="h-tmux">tmux</span>
    <button id="theme-toggle" class="iconbtn" title="Switch light / dark" aria-label="Switch theme">◐</button>
  </div>
</header>

<div id="shell">
  <aside id="sidebar">
    <a href="#" id="lnk-create" class="cta">＋ New sandbox</a>
    <div class="side-h">Containers <span class="cnt" id="cnt-c"></span></div>
    <div class="items" id="items-c"></div>
    <div class="side-h">VMs · libvirt <span class="cnt" id="cnt-vl"></span></div>
    <div class="items" id="items-vl"></div>
    <div class="side-h">VMs · docker (hotbedvmd) <span class="cnt" id="cnt-vd"></span></div>
    <div class="items" id="items-vd"></div>
    <div id="sidebar-foot">
      <a href="#" id="lnk-burn">Burn - tokens &amp; budget</a>
      <a href="#" id="lnk-config">OpenCode config</a>
      <a href="#" id="lnk-help">Help &amp; CLI</a>
    </div>
  </aside>

  <main id="main">
    <div id="empty">
      <div class="emptycard">
        <h3>No sandbox selected</h3>
        <p>Select a sandbox from the sidebar, or create a new one.</p>
        <button class="cta2" data-create>＋ Create sandbox</button>
      </div>
    </div>
    <section id="pane" hidden>
      <div id="pane-head">
        <div>
          <h2 id="d-name"></h2>
          <div id="d-sub"></div>
        </div>
        <div id="d-actions">
          <button id="btn-attach" class="primary">Open Terminal</button>
          <button id="btn-oc">OpenCode in tmux</button>
          <button id="btn-copy">Copy Connection</button>
          <button id="btn-start">Start</button>
          <button id="btn-stop" class="danger">Stop</button>
          <button id="btn-del" class="danger">Delete</button>
        </div>
      </div>

      <div id="tabs">
        <button class="tab active" data-tab="overview">Overview</button>
        <button class="tab" data-tab="logs">Logs</button>
        <button class="tab" data-tab="conn">Connection</button>
        <button class="tab" data-tab="burn">Burn</button>
        <button class="tab" data-tab="dns" id="tabbtn-dns">DNS</button>
      </div>

      <div id="tab-overview" class="tabpane">
        <div id="ov-stats" class="cardrow"></div>
        <div id="ov-cards" class="cardrow wrap"></div>
      </div>

      <div id="tab-logs" class="tabpane" hidden>
        <div class="logbar"><label><input type="checkbox" id="log-follow" checked> follow</label>
          <span id="log-state"></span><button id="log-clear">clear</button></div>
        <pre id="logbox"></pre>
      </div>

      <div id="tab-conn" class="tabpane">
        <div class="hint">Primary = SSH (paste anywhere). Persistent tmux session survives detachment; &ldquo;Open Terminal&rdquo; attaches to it in Windows Terminal.</div>
        <div id="conn-rows"></div>
      </div>

      <div id="tab-burn" class="tabpane">
        <div style="display:flex;align-items:center;justify-content:space-between;gap:var(--s2)">
          <div class="hint" style="flex:1">Per-sandbox token ledger from the proxy. Upstream-reported usage where the provider gives it; a bytes/4 estimate (marked est) where it doesn't. Auto-updates every 5 s.</div>
          <button class="tool" id="burn-reload" title="re-read the burn ledger now">&#8635; reload</button>
        </div>
        <div id="burn-hbd"></div>
      </div>

      <div id="tab-dns" class="tabpane" hidden>
        <div id="dns-body"></div>
      </div>
    </section>

    <section id="global" hidden>
      <div id="pane-head">
        <div><h2 id="glob-title"></h2><div id="glob-sub" class="dsub"></div></div>
        <div id="glob-actions"></div>
      </div>
      <div id="glob-body"></div>
    </section>
  </main>
</div>

<div id="toasts"></div>
<script src="/static/app.js"></script>
</body>
</html>
`

const styleCSS = `/* Hotbed design system - hand-rolled, no framework */
:root{
  --bg:#0d1015; --bg2:#131722; --bg3:#1a2230; --bg4:#222c3d;
  --fg:#e7ecf3; --dim:#8d99ab; --faint:#5d6b7e;
  --acc:#5b8cff; --acc2:#7aa5ff; --acc-soft:rgba(91,140,255,.16);
  --grn:#4cc38a; --red:#e5484d; --yel:#e7a43b;
  --line:#232b3a; --line2:#2e3a4e;
  --field:#0b0f16; --well:#0a0d12;
  --mono:ui-monospace,"SFMono-Regular",Menlo,Consolas,monospace;
  --sans:system-ui,-apple-system,"Segoe UI",Roboto,"Inter",sans-serif;
  --r1:6px; --r2:10px; --r3:14px;
  --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s5:24px; --s6:32px;
  --shadow:0 10px 30px rgba(2,6,14,.5);
  color-scheme:dark;
}
html[data-theme=light]{
  --bg:#f4f5f7; --bg2:#ffffff; --bg3:#eef1f5; --bg4:#e3e7ee;
  --fg:#1b2430; --dim:#4c5768; --faint:#8a94a4;
  --acc:#2f66e8; --acc2:#2f66e8; --acc-soft:rgba(47,102,232,.10);
  --grn:#1a7f4b; --red:#d33a3f; --yel:#a86310;
  --line:#e3e7ee; --line2:#d3d9e4;
  --field:#ffffff; --well:#f2f4f7;
  --shadow:0 8px 22px rgba(20,30,50,.10);
  color-scheme:light;
}
*{box-sizing:border-box}
html,body{height:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:13.5px/1.5 var(--sans);display:flex;flex-direction:column;-webkit-font-smoothing:antialiased}
[hidden]{display:none !important}
::-webkit-scrollbar{width:10px;height:10px}
::-webkit-scrollbar-thumb{background:var(--line2);border-radius:8px;border:2px solid transparent;background-clip:content-box}
::-webkit-scrollbar-track{background:transparent}
:focus-visible{outline:2px solid var(--acc);outline-offset:1px}

/* ---------- topbar ---------- */
#topbar{display:flex;align-items:center;justify-content:space-between;gap:var(--s4);padding:0 var(--s5);height:52px;background:var(--bg2);border-bottom:1px solid var(--line);flex:none}
#brand{display:flex;align-items:center;gap:var(--s2);font-weight:600}
#brand .logo{background:linear-gradient(135deg,var(--acc),#8f6bff);color:#fff;padding:.12rem .5rem;border-radius:var(--r1);font-weight:800;font-size:.82rem;letter-spacing:.04em}
#brand .brandname{font-size:.95rem}
#ver{color:var(--faint);font-weight:400;font-size:.78rem;margin-left:var(--s2)}
#health{display:flex;gap:var(--s2)}
.hbadge{display:inline-flex;align-items:center;gap:6px;font-size:.72rem;color:var(--dim);border:1px solid var(--line);padding:.22rem .6rem;border-radius:999px;background:var(--bg3)}
.hbadge::before{content:"";width:6px;height:6px;border-radius:50%;background:var(--faint);flex:none}
.hbadge.up{color:var(--grn);border-color:rgba(76,195,138,.4);background:rgba(76,195,138,.08)}
.hbadge.up::before{background:var(--grn);box-shadow:0 0 6px rgba(76,195,138,.8)}
.hbadge.down{color:var(--red);border-color:rgba(229,72,77,.4);background:rgba(229,72,77,.08)}
.hbadge.down::before{background:var(--red)}

/* ---------- sidebar ---------- */
#shell{display:flex;flex:1;min-height:0}
#sidebar{width:268px;min-width:236px;flex:none;background:var(--bg2);border-right:1px solid var(--line);overflow-y:auto;padding:var(--s4) var(--s3) var(--s4);display:flex;flex-direction:column}
.cta{display:flex;align-items:center;justify-content:center;gap:var(--s2);background:linear-gradient(135deg,var(--acc),#6d79ff);color:#fff;font-weight:600;font-size:.84rem;text-decoration:none;border-radius:var(--r2);padding:.6rem .8rem;box-shadow:0 4px 14px rgba(91,140,255,.25)}
.cta:hover{filter:brightness(1.08)}
.side-h{color:var(--faint);text-transform:uppercase;font-size:.66rem;font-weight:700;letter-spacing:.09em;margin:var(--s5) var(--s2) var(--s2);display:flex;align-items:center;gap:var(--s1)}
.side-h:first-of-type{margin-top:var(--s4)}
.cnt{color:var(--acc);font-weight:700;min-width:.5rem}
.items{display:flex;flex-direction:column;gap:2px}
.item{padding:.5rem .6rem;border-radius:var(--r2);cursor:pointer;display:flex;flex-direction:column;gap:.3rem;border:1px solid transparent}
.item:hover{background:var(--bg3)}
.item.sel{background:var(--acc-soft);border-color:rgba(91,140,255,.35);box-shadow:inset 2px 0 0 var(--acc)}
.item .nmrow{display:flex;align-items:center;gap:var(--s2)}
.item .nmrow b{flex:1;min-width:0;font-weight:600;font-size:.84rem;line-height:1.25;overflow-wrap:break-word;word-break:break-word}
.item .subrow{display:flex;align-items:center;gap:var(--s2)}
.item .subrow small{flex:1;min-width:0;color:var(--faint);font-size:.7rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.item .meters{font-size:.66rem;color:var(--faint);text-align:right;line-height:1.3;flex:none}
.pill{font-size:.62rem;border-radius:999px;padding:.1rem .42rem;font-weight:700;color:var(--dim);background:var(--bg4);border:1px solid var(--line2);text-transform:uppercase;letter-spacing:.03em;align-self:center}
.pill.running{background:rgba(76,195,138,.12);color:var(--grn);border-color:rgba(76,195,138,.4)}
.pill.created{background:rgba(91,140,255,.12);color:var(--acc2);border-color:rgba(91,140,255,.4)}
.pill.paused,.pill.restarting{background:rgba(231,164,59,.12);color:var(--yel);border-color:rgba(231,164,59,.4)}
.pill.exited,.pill.dead,.pill.stopped{background:rgba(229,72,77,.1);color:var(--red);border-color:rgba(229,72,77,.35)}
.pill.shutoff{background:rgba(231,164,59,.08);color:var(--yel);border-color:rgba(231,164,59,.25)}
.pill.booting{background:rgba(231,164,59,.14);color:var(--yel);border-color:rgba(231,164,59,.5);animation:hotbedpulse 1.5s ease-in-out infinite}
.pill.stopping,.pill.starting{background:rgba(231,164,59,.14);color:var(--yel);border-color:rgba(231,164,59,.5);animation:hotbedpulse 1.5s ease-in-out infinite}
@keyframes hotbedpulse{from{opacity:1}50%{opacity:.55}to{opacity:1}}
/* ---------- first-load spinner (until the first inventory snapshot) ---------- */
.fspin{display:flex;align-items:center;gap:8px;color:var(--faint);font-size:.74rem;padding:12px 2px;letter-spacing:.02em}
.spin{width:13px;height:13px;border:2px solid var(--line2);border-top-color:var(--acc);border-radius:50%;animation:hotbedspin .8s linear infinite;flex:none}
@keyframes hotbedspin{to{transform:rotate(360deg)}}

/* ---------- overview (host & sandboxes start page) ---------- */
.ov-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(290px,1fr));gap:var(--s4)}
/* the four host metric cards keep one row on wide screens;
   narrow windows fall back to the auto-fill grid */
.ov-grid.ov-row{grid-template-columns:repeat(4,minmax(0,1fr))}
@media (max-width:1100px){ .ov-grid.ov-row{grid-template-columns:repeat(auto-fill,minmax(290px,1fr))} }
.ov-card{background:var(--bg2);border:1px solid var(--line);border-radius:var(--r2);padding:var(--s4) var(--s4) var(--s4) var(--s4)}
/* slim per-mount disk strip under the metric row */
.ov-disks{display:flex;flex-wrap:wrap;gap:var(--s4);margin-top:var(--s4)}
.ov-disk{flex:1 1 220px;display:flex;align-items:center;gap:var(--s3);background:var(--bg2);
  border:1px solid var(--line);border-radius:var(--r2);padding:.55rem .8rem}
.ov-disk .mnt{color:var(--dim);font-size:.74rem;white-space:nowrap;text-transform:uppercase;letter-spacing:.05em;max-width:9rem;overflow:hidden;text-overflow:ellipsis}
.ov-disk .pct{color:var(--dim);font-size:.74rem;flex:none;min-width:2.6rem;text-align:right}
.ov-disk .ov-bar{flex:1;margin-top:0}
/* clickable per-sandbox cards */
.ovhotbed{cursor:pointer;transition:border-color .15s, transform .15s}
.ovhotbed:hover{border-color:var(--acc);transform:translateY(-1px)}
#brand{cursor:pointer}
.ov-h{display:flex;justify-content:space-between;align-items:baseline;color:var(--dim);font-size:.72rem;letter-spacing:.06em;text-transform:uppercase;margin-bottom:var(--s3)}
.ov-h b{color:var(--fg);font-size:1.35rem;letter-spacing:0;text-transform:none}
.ov-bar{height:7px;border-radius:4px;background:var(--bg3);overflow:hidden;margin-top:var(--s3)}
.ov-bar span{display:block;height:100%;border-radius:4px;background:linear-gradient(90deg,var(--grn),#7ee2a8)}
.ov-bar span.warn{background:linear-gradient(90deg,#e7a43b,#f0c268)}
.ov-bar span.hot{background:linear-gradient(90deg,#e05a5a,#ef8585)}
.ov-kv{display:flex;justify-content:space-between;gap:var(--s3);padding:.24rem 0;font-size:.78rem;border-bottom:1px dashed var(--line)}
.ov-kv:last-child{border-bottom:none}
.ov-kv span:first-child{color:var(--dim)}
.hbk{display:inline-flex;align-items:center;gap:.45em;padding:.14rem .5rem;border:1px solid var(--line2);border-radius:999px;font-size:.72rem;color:var(--dim);margin:.15rem .2rem .15rem 0}
.hbk.on{color:var(--grn);border-color:rgba(76,195,138,.5)}
.hbk.off{color:var(--faint)}
#sidebar-foot{margin-top:auto;padding-top:var(--s5);display:flex;flex-direction:column;gap:2px;border-top:1px solid var(--line)}
#sidebar-foot a{color:var(--dim);font-size:.76rem;text-decoration:none;padding:.4rem .6rem;border-radius:var(--r1)}
#sidebar-foot a:hover{color:var(--fg);background:var(--bg3)}

/* ---------- main / panes ---------- */
#main{flex:1;display:flex;flex-direction:column;min-width:0;overflow:auto}
#empty{color:var(--dim);margin:auto;text-align:center}
.emptycard h3{font-size:1.05rem;font-weight:600;color:var(--fg);margin:0 0 var(--s2)}
.emptycard p{font-size:.82rem;margin:0 0 var(--s4)}
.cta2{background:linear-gradient(135deg,var(--acc),#6d79ff);color:#fff;border:none;font-weight:600;border-radius:var(--r2);padding:.6rem 1.1rem;font-size:.84rem;cursor:pointer;box-shadow:0 4px 14px rgba(91,140,255,.25)}
.cta2:hover{filter:brightness(1.08)}
#pane,#global{padding:var(--s5) var(--s6) var(--s6);max-width:1280px;width:100%;flex:none}
#global{margin:0 auto}
#pane-head{display:flex;justify-content:space-between;align-items:flex-start;gap:var(--s4);margin-bottom:var(--s4)}
#pane-head h2{margin:0;font-size:1.2rem;font-weight:650;letter-spacing:-.01em}
#d-sub,.dsub{color:var(--faint);font-size:.78rem;margin-top:.25rem}
#d-actions{display:flex;gap:var(--s2);flex-wrap:wrap;justify-content:flex-end}

/* ---------- controls ---------- */
button{background:var(--bg3);color:var(--fg);border:1px solid var(--line2);border-radius:var(--r1);padding:.45rem .8rem;font-size:.78rem;font-weight:500;cursor:pointer;font-family:var(--sans);transition:border-color .12s,background .12s}
button:hover{border-color:var(--acc);background:var(--bg4)}
button.primary{background:linear-gradient(135deg,var(--acc),#6d79ff);border-color:transparent;color:#fff;font-weight:600}
button.primary:hover{filter:brightness(1.08)}
/* in-flight state: the create wizard's submit (and any button given .busy)
   cannot be re-triggered while busy - the spinner ring says "working". */
button.busy{opacity:.72;pointer-events:none;cursor:progress}
button.busy::after{content:"";display:inline-block;width:.85em;height:.85em;margin-left:.5em;border:2px solid rgba(255,255,255,.45);border-top-color:currentColor;border-radius:50%;vertical-align:-.12em;animation:hotbedspin .8s linear infinite}
button.danger:hover{border-color:var(--red);color:var(--red);background:rgba(229,72,77,.08)}
button:disabled{opacity:.4;cursor:default;pointer-events:none}
button.tool{background:var(--bg3);border:1px solid var(--line2);border-radius:var(--r1);padding:.28rem .6rem;font-size:.74rem}
.iconbtn{background:none;border:none;color:var(--faint);font-size:.8rem;padding:.3rem .5rem;border-radius:var(--r1);line-height:1}
.iconbtn:hover{color:var(--red);background:rgba(229,72,77,.1);border:none}
input,select,textarea{font-family:var(--sans)}
input[type=text],input:not([type]),select,textarea{background:var(--field);border:1px solid var(--line2);color:var(--fg);border-radius:var(--r1);padding:.4rem .6rem;font-size:.8rem;height:32px}
input:focus,select:focus,textarea:focus{border-color:var(--acc);outline:none;box-shadow:0 0 0 3px var(--acc-soft)}
input::placeholder,textarea::placeholder{color:var(--faint)}
select{appearance:none;-webkit-appearance:none;padding-right:1.6rem;background-image:url("data:image/svg+xml;charset=utf8,%3Csvg xmlns='http://www.w3.org/2000/svg' width='10' height='6'%3E%3Cpath d='M1 1l4 4 4-4' fill='none' stroke='%238d99ab' stroke-width='1.6' stroke-linecap='round'/%3E%3C/svg%3E");background-repeat:no-repeat;background-position:right .55rem center;cursor:pointer}
#tabs{display:flex;gap:var(--s4);border-bottom:1px solid var(--line);margin:var(--s3) 0 var(--s4)}
.tab{background:none;border:none;border-radius:0;color:var(--dim);padding:.45rem .1rem;font-size:.8rem;font-weight:500;border-bottom:2px solid transparent}
.tab:hover{color:var(--fg);background:none}
.tab.active{color:var(--acc2);border-bottom-color:var(--acc);font-weight:600}
.tabpane{min-height:220px}

/* ---------- cards ---------- */
.cardrow{display:grid;grid-template-columns:repeat(4,1fr);gap:var(--s3)}
.cardrow.wrap{grid-template-columns:repeat(2,1fr)}
.card{background:var(--bg2);border:1px solid var(--line);border-radius:var(--r2);padding:.8rem .9rem}
.card h4{margin:0 0 .4rem;font-size:.68rem;color:var(--faint);text-transform:uppercase;letter-spacing:.08em;font-weight:700}
.card .v{font-size:.92rem;word-break:break-all}
.card .v.small,.card li{font-size:.78rem;color:var(--fg)}
.card ul{margin:.15rem 0 0 .9rem;padding:0}
.card .sntl li{font-family:var(--mono);font-size:.74rem;padding:.12rem 0}
.stat .v{font-family:var(--mono);font-size:1rem;font-weight:600}
.hint{color:var(--faint);font-size:.8rem;margin-bottom:var(--s3)}
.dim,.small{color:var(--dim)}
.small{font-size:.76rem}
.err,.iserr{color:var(--red)}
.iserr{font-size:.76rem}

/* ---------- logs ---------- */
.logbar{display:flex;align-items:center;gap:var(--s3);color:var(--faint);font-size:.76rem;margin-bottom:var(--s2)}
.logbar button{padding:.25rem .6rem;font-size:.72rem}
#logbox{background:var(--well);border:1px solid var(--line);border-radius:var(--r2);padding:var(--s3) var(--s4);height:min(52vh,640px);overflow:auto;font:11.5px/1.55 var(--mono);white-space:pre-wrap;word-break:break-word;margin:0}

/* ---------- connection rows ---------- */
#conn-rows .row{display:flex;align-items:center;gap:var(--s3);background:var(--bg2);border:1px solid var(--line);border-radius:var(--r2);padding:.55rem .7rem;margin-bottom:var(--s2)}
#conn-rows .lbl{min-width:150px;color:var(--faint);font-size:.72rem;text-transform:uppercase;letter-spacing:.05em;font-weight:600}
#conn-rows code{flex:1;font:.76rem var(--mono);overflow-x:auto;white-space:nowrap;color:var(--acc2)}

/* ---------- tables (burn etc.) ---------- */
.tbl{width:100%;border-collapse:collapse;font-size:.78rem}
.tbl th{color:var(--faint);text-align:left;font-weight:600;border-bottom:1px solid var(--line2);padding:.5rem .55rem;text-transform:uppercase;font-size:.66rem;letter-spacing:.06em}
.tbl td{padding:.5rem .55rem;border-bottom:1px solid var(--line);font-family:var(--mono);font-size:.76rem}
.tbl tbody tr:hover td{background:rgba(91,140,255,.04)}
.tbl td.actions{display:flex;gap:var(--s1)}
.subwrap{margin:var(--s1) 0 var(--s2) var(--s1);font-size:.75rem}
.subwrap .kv{color:var(--dim)}
.recent td{color:var(--dim)}
input.money{width:78px;background:var(--field);border:1px solid var(--line2);color:var(--fg);padding:.3rem .45rem;border-radius:var(--r1);font:.75rem var(--mono)}
input.money.big{width:100%}
textarea#cfg-editor{width:100%;min-height:54vh;background:var(--well);color:var(--fg);border:1px solid var(--line2);border-radius:var(--r2);padding:var(--s4);font:.78rem/1.55 var(--mono);white-space:pre;resize:vertical}
.warnline{color:var(--yel);font-size:.78rem;margin:var(--s2) 0}

/* ---------- create wizard: sandbox-kind radios + VM sizing ---------- */
.cw-kind{display:flex;gap:var(--s3);flex-wrap:wrap}
.cw-kind label{margin-right:var(--s3)}

/* ---------- burn (bigger view): stat cards, stale greying, pins ---------- */
.burnbar{display:flex;gap:var(--s2);align-items:center;flex-wrap:wrap;margin:0 0 var(--s3)}
.bstat{flex:1 1 150px;min-width:150px;padding:.7rem .9rem;border:1px solid var(--line);border-radius:var(--r2);background:var(--bg2)}
.bstat .v{font:700 22px/1.25 var(--mono);color:var(--fg)}
.bstat .k{font-size:.64rem;color:var(--faint);text-transform:uppercase;letter-spacing:.06em;margin-top:.25rem}
.bstat.warn .v{color:var(--yel)}
.burntools{display:flex;gap:var(--s2);align-items:center;flex-wrap:wrap;margin:0 0 var(--s2)}
.burntools label{display:inline-flex;gap:.4rem;align-items:center;color:var(--dim);font-size:.76rem;cursor:pointer;user-select:none}
tr.rowdim td{opacity:.42}
tr.rowdim td:first-child{opacity:.95}
#burntbl tr[data-hidden]{display:none}
/* DNS tab */
.dnsst{font-size:.6rem;border:1px solid var(--line2);color:var(--dim);border-radius:999px;padding:.05rem .45rem;text-transform:uppercase;letter-spacing:.05em;font-family:var(--sans);font-weight:700}
.dnsst.ok{color:var(--grn);border-color:rgba(76,195,138,.5)}
.dnsst.bad{color:var(--red);border-color:rgba(229,72,77,.45)}
.dnstools{display:flex;gap:var(--s3);align-items:center;flex-wrap:wrap;margin:0 0 var(--s3)}
.dnstools label{display:inline-flex;gap:.4rem;align-items:center;color:var(--dim);font-size:.76rem;cursor:pointer;user-select:none}
.dnstools .grow{flex:1}
#dns-filter{width:220px;max-width:100%}
#dns-tbl td.tg{width:46px}
#dns-tbl .via{color:var(--faint);font-size:.68rem;margin-left:.4rem;font-family:var(--sans)}
/* iOS-style toggle switch (appearance of #burn-show-stale) */
.ios-toggle{position:relative;display:inline-block;width:38px;height:22px;vertical-align:-5px;margin-right:.45rem}
.ios-toggle input{position:absolute;inset:0;opacity:0;margin:0;cursor:pointer;z-index:2}
.ios-toggle .track{position:absolute;inset:0;background:rgba(255,255,255,.10);border:1px solid var(--line2);border-radius:999px;transition:background .16s ease,border-color .16s ease}
.ios-toggle .knob{position:absolute;top:2px;left:2px;width:16px;height:16px;border-radius:50%;background:var(--faint);box-shadow:0 1px 3px rgba(0,0,0,.4);transition:left .16s ease,background .16s ease}
.ios-toggle input:checked ~ .track{background:rgba(76,195,138,.28);border-color:rgba(76,195,138,.55)}
.ios-toggle input:checked ~ .knob{left:18px;background:var(--grn)}
.ios-toggle input:checked:hover ~ .track{background:rgba(76,195,138,.4)}
.ios-toggle input:disabled{cursor:not-allowed}
.ios-toggle input:disabled ~ .track,.ios-toggle input:disabled ~ .knob{opacity:.45}
.chipmuted{font-size:.6rem;color:var(--yel);border:1px solid rgba(231,164,59,.5);border-radius:999px;padding:.05rem .45rem;margin-left:.4rem;text-transform:uppercase;letter-spacing:.05em;vertical-align:middle}
.starpin{background:none;border:none;color:var(--faint);cursor:pointer;font-size:1rem;padding:0 .15rem;line-height:1}
.starpin:hover{color:var(--yel)}
.starpin.on{color:var(--yel)}

/* ---------- toasts ---------- */
#toasts{position:fixed;right:var(--s4);bottom:var(--s4);display:flex;flex-direction:column;gap:var(--s2);z-index:50}
.toast{background:var(--bg3);border:1px solid var(--line2);border-left:4px solid var(--acc);border-radius:var(--r2);padding:.7rem 1rem;font-size:.84rem;font-weight:500;line-height:1.4;max-width:540px;min-width:280px;box-shadow:0 12px 30px rgba(2,6,16,.55)}
.toast.ok{border-left-color:var(--grn)}
.toast.err{border-left-color:var(--red)}
.toast.pend{border-left-color:var(--yel);animation:hotbedpend 1.2s ease-in-out infinite}
@keyframes hotbedpend{0%,100%{box-shadow:0 12px 30px rgba(2,6,16,.55)}50%{box-shadow:0 12px 30px rgba(2,6,16,.55),0 0 0 3px rgba(231,164,59,.28)}}

/* ---------- create wizard ---------- */
.cw{max-width:880px}
.cw-card{background:var(--bg2);border:1px solid var(--line);border-radius:var(--r3);padding:var(--s2) var(--s5) var(--s5);box-shadow:var(--shadow)}
.cw-sec{padding:var(--s4) 0;border-bottom:1px dashed var(--line2)}
.cw-sec:last-of-type{border-bottom:none}
.cw-sec-t{display:flex;align-items:center;gap:var(--s2);font-size:.7rem;font-weight:700;text-transform:uppercase;letter-spacing:.1em;color:var(--dim);margin-bottom:var(--s3)}
.cw-sec-t::before{content:"";width:3px;height:13px;border-radius:2px;background:linear-gradient(var(--acc),#8f6bff);flex:none}
.cw-grid{display:grid;gap:var(--s4)}
.cw-grid.two,.cw-grid .two{grid-template-columns:repeat(2,minmax(0,1fr))}
.field{display:flex;flex-direction:column;min-width:0}
.field label{font-size:.7rem;font-weight:600;color:var(--dim);letter-spacing:.03em;margin-bottom:var(--s1)}
.fm{font-size:.72rem;color:var(--faint);margin-top:var(--s1)}
.fm.iserr{color:var(--red)}
label.check,label.ro{display:flex;align-items:center;gap:var(--s2);cursor:pointer;font-size:.8rem;color:var(--fg);user-select:none}
label.check input,label.ro input{position:absolute;opacity:0;pointer-events:none}
label.check .box,label.ro .box{width:15px;height:15px;border-radius:4px;border:1px solid var(--line2);background:var(--field);display:inline-block;position:relative;flex:none;transition:background .12s,border-color .12s}
label.check input:checked~.box,label.ro input:checked~.box{background:var(--acc);border-color:var(--acc)}
label.check input:checked~.box::after,label.ro input:checked~.box::after{content:"";position:absolute;left:4.6px;top:1.6px;width:4px;height:8px;border:solid #fff;border-width:0 2px 2px 0;transform:rotate(45deg)}
label.check:hover .box,label.ro:hover .box{border-color:var(--acc)}
.rows-t{font-size:.74rem;color:var(--dim);margin:var(--s3) 0 var(--s2)}
.rows{display:flex;flex-direction:column;gap:var(--s2)}
.drow{display:flex;align-items:center;gap:var(--s2);background:var(--field);border:1px solid var(--line);border-radius:var(--r2);padding:.45rem .6rem}
.drow:focus-within{border-color:var(--acc);box-shadow:0 0 0 3px var(--acc-soft)}
.drow input{background:none;border:none;box-shadow:none;height:26px;padding:.1rem .2rem;font-size:.8rem;color:var(--fg);min-width:0}
.drow input:focus{outline:none;box-shadow:none;border:none}
.dsep{color:var(--faint);font-family:var(--mono);flex:none}
.drow .iconbtn{margin-left:auto;flex:none}
.phost{width:120px;text-align:right;font-family:var(--mono)}
.pct{width:140px;font-family:var(--mono)}
.ekey{width:210px;font-family:var(--mono);font-weight:600}
.eval{flex:1;font-family:var(--mono)}
.mwrap{display:flex;align-items:center;gap:var(--s2);flex:1;min-width:0}
.msrc{flex:1;min-width:180px;font-family:var(--mono)}
.mtgt{flex:1;min-width:160px;font-family:var(--mono)}
button.browse{padding:.3rem .6rem;font-size:.72rem;color:var(--acc2);background:rgba(91,140,255,.08);border-color:rgba(91,140,255,.3)}
.addrow{width:100%;background:none;border:1px dashed var(--line2);color:var(--faint);border-radius:var(--r2);padding:.45rem;font-size:.76rem;margin-top:var(--s2)}
.addrow:hover{color:var(--acc);border-color:var(--acc);background:var(--acc-soft)}
.cw-bar{display:flex;align-items:center;gap:var(--s3);margin-top:var(--s4)}
.cw-bar button.primary{padding:.6rem 1.3rem;font-size:.84rem;border-radius:var(--r2)}
.cw-msg{font-size:.8rem;color:var(--dim)}

/* ---------- folder browser modal ---------- */
#fs-modal{position:fixed;inset:0;background:rgba(4,7,12,.65);backdrop-filter:blur(3px);display:none;align-items:center;justify-content:center;z-index:100}
#fs-modal.open{display:flex}
/* ---------- generic ask/confirm modal (replaces abrupt window.confirm) ---------- */
#ask-modal{position:fixed;inset:0;background:rgba(4,7,12,.65);backdrop-filter:blur(3px);display:none;align-items:center;justify-content:center;z-index:110}
#ask-modal.open{display:flex}
.ask-card{width:420px;max-width:92vw;background:var(--bg2);border:1px solid var(--line2);border-radius:var(--r3);box-shadow:var(--shadow);overflow:hidden}
.ask-head{display:flex;justify-content:space-between;align-items:center;padding:var(--s3) var(--s4);border-bottom:1px solid var(--line)}
.ask-title{font-weight:600;font-size:.85rem}
.ask-body{padding:var(--s4);font-size:.82rem;color:var(--dim);line-height:1.5;max-height:50vh;overflow-y:auto}
.ask-body b,.ask-body strong{color:var(--fg)}
.ask-body .mono{font-family:var(--mono);font-size:.78rem;color:var(--fg);background:var(--bg3);border-radius:var(--r1);padding:.1rem .35rem}
.ask-foot{display:flex;justify-content:flex-end;gap:var(--s2);padding:var(--s3) var(--s4);border-top:1px solid var(--line)}
.ask-foot button{min-width:6.5rem}
.ask-danger{color:#fff;background:rgba(224,90,90,.85);border-color:transparent}
.ask-danger:hover{background:rgba(224,90,90,1)}
.ask-att{display:flex;gap:var(--s2);align-items:flex-start;margin-top:var(--s3);padding:.5rem .7rem;border-radius:var(--r2);font-size:.74rem;background:rgba(231,164,59,.08);color:var(--dim)}
/* help page: CLI command cells + section headings */
.mono{font-family:var(--mono);font-size:.8em}
.tbl code{font-family:var(--mono);font-size:.74rem;color:var(--acc2);background:var(--bg3);padding:.08rem .35rem;border-radius:var(--r1);white-space:nowrap}
.hsec{font-size:.72rem;color:var(--faint);text-transform:uppercase;letter-spacing:.08em;margin:20px 0 6px}
.fs-dialog{width:580px;max-width:92vw;height:460px;max-height:78vh;display:flex;flex-direction:column;background:var(--bg2);border:1px solid var(--line2);border-radius:var(--r3);box-shadow:var(--shadow);overflow:hidden}
.fs-head{display:flex;justify-content:space-between;align-items:center;padding:var(--s3) var(--s4);border-bottom:1px solid var(--line)}
.fs-title{font-weight:600;font-size:.85rem}
.fs-pathbar{display:flex;gap:var(--s2);padding:var(--s3) var(--s4) var(--s1)}
#fs-cur{flex:1;font-family:var(--mono);font-size:.76rem;min-width:0}
.fs-crumb{display:flex;flex-wrap:wrap;align-items:center;gap:1px;padding:var(--s2) var(--s4) var(--s2)}
.crumb{background:none;border:none;color:var(--dim);cursor:pointer;padding:.2rem .4rem;border-radius:var(--r1);font-size:.72rem}
.crumb:hover{color:var(--acc);background:var(--acc-soft)}
.crumb.cur{color:var(--fg);font-weight:600}
.csep{color:var(--faint);font-size:.72rem}
.fs-list{flex:1;overflow-y:auto;border-top:1px solid var(--line);padding:var(--s2)}
.fs-row{display:flex;align-items:center;gap:var(--s2);padding:.4rem .55rem;border-radius:var(--r1);cursor:pointer;font-size:.8rem}
.fs-row:hover{background:var(--bg3)}
.fs-row .grow{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.fs-row.file{color:var(--dim)}
.fs-row.file .fi{color:var(--faint)}
.fs-row.dir .fi{color:var(--acc2)}
.fs-row.sel{background:var(--acc-soft);box-shadow:inset 2px 0 0 var(--acc)}
.chev{background:none;border:none;color:var(--faint);padding:.1rem .4rem;font-size:.85rem;border-radius:4px;opacity:0}
.fs-row:hover .chev{opacity:.8}
.chev:hover{color:var(--acc)}
.fi{flex:none}
.fs-empty{padding:var(--s6) var(--s4);color:var(--faint);text-align:center;font-size:.78rem}
.fs-foot{display:flex;align-items:center;gap:var(--s2);padding:var(--s3) var(--s4);border-top:1px solid var(--line)}
.fs-sel{flex:1;font-size:.72rem;color:var(--faint);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-family:var(--mono)}

@media (max-width:1120px){
  #pane,#global{padding:var(--s4) var(--s4) var(--s5)}
  .cardrow{grid-template-columns:repeat(2,1fr)}
  .cardrow.wrap{grid-template-columns:1fr}
  .cw-grid.two{grid-template-columns:1fr}
}
@media (max-width:760px){
  #sidebar{width:220px}
  #topbar{padding:0 var(--s3)}
  .hbadge{display:none}
}
`

const appJS = `'use strict';
/* Hotbed dashboard - vanilla JS, SSE-driven. No build step. */
var state = { list: [], sel: null, es: null, esLogs: null, health: null, view: 'overview', firstData: false, pending: {} };

function $(id){ return document.getElementById(id); }
function h(tag, attrs, html){ var el=document.createElement(tag); if(attrs) for(var k in attrs) el.setAttribute(k,attrs[k]); if(html!=null) el.innerHTML=html; return el; }
function esc(s){ return (s==null?'':String(s)).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
function toast(msg, kind){
  var t=h('div',{class:'toast'+(kind?' '+kind:'')},esc(msg));
  $('toasts').appendChild(t);
  if(kind!=='pend'){ setTimeout(function(){ t.remove(); }, kind==='err'?7600:4200); } // pend toasts live until their action settles
  return t;
}
function copy(text){
  if(navigator.clipboard && window.isSecureContext){
    navigator.clipboard.writeText(text).then(function(){ toast('Copied to clipboard','ok'); },
      function(){ legacyCopy(text); });
  }else{ legacyCopy(text); }
}
// browsers only grant navigator.clipboard on secure contexts (https, or
// localhost) - over plain http on a LAN box, execCommand('copy') still works;
// when even that fails the toast names the actual cure (HOTBEDD_TLS / https).
function legacyCopy(text){
  var ta=document.createElement('textarea'); ta.value=text;
  ta.style.position='fixed'; ta.style.opacity='0';
  document.body.appendChild(ta); ta.focus(); ta.select();
  var okc=false; try{ okc=document.execCommand('copy'); }catch(e){}
  document.body.removeChild(ta);
  if(okc){ toast('Copied to clipboard','ok'); }
  else{ toast('Clipboard needs a secure context - open via https:// (HOTBEDD_TLS=1) or localhost','err'); }
}

/* ---------- list rendering ---------- */
function groupFor(sb){
  if(sb.kind==='container') return 'items-c';
  if(sb.kind==='vm') return sb.backend==='docker' ? 'items-vd' : 'items-vl';
  return null;
}
function pillFor(sb){
  var pend=(state.pending||{})[sb.kind+':'+sb.name];
  if(pend==='stop')  return '<span class="pill stopping">stopping…</span>';
  if(pend==='start') return '<span class="pill starting">starting…</span>';
  var st=(sb.state||'').toLowerCase();
  var s=st.replace(/[\s-]+/g,'');
  return '<span class="pill '+esc(s)+'">'+esc(sb.state)+'</span>';
}
function meters(sb){
  var out=[];
  if(sb.cpuPercent!=null && sb.cpuPercent>0.005) out.push('cpu '+sb.cpuPercent.toFixed(1)+'%');
  if(sb.memUsage) out.push('mem '+esc(sb.memUsage));
  return out.join('<br>');
}
function renderItem(sb){
  var el=h('div',{class:'item'+(state.sel===keyOf(sb)?' sel':''),'data-key':keyOf(sb)});
  el.title=sb.name; // hover reveals the full name even when the item wraps
  var sub=sb.kind==='container' ? (sb.stack||'container')+' · '+short(sb.project||sb.image||'')
        : (sb.backend==='docker'?'hotbedvmd':sb.backend)+(sb.ports&&sb.ports.length?' · '+sb.ports[0]:'');
  el.innerHTML='<div class="nmrow"><b>'+esc(sb.name)+'</b>'+pillFor(sb)+'</div>'+
    '<div class="subrow"><small>'+esc(sub)+'</small><div class="meters">'+meters(sb)+'</div></div>';
  el.addEventListener('click', function(){ select(sb); });
  return el;
}
function short(s){ return (s||'').replace(/^\/home\//,'~/'); }
function keyOf(sb){ return sb.kind+'/'+sb.name; }
/* persist the front-most view so a page reload lands where the user left */
function saveView(){
  try{
    localStorage.setItem('hotbed-view', JSON.stringify({
      view: state.view, sel: state.sel,
      tab: (typeof activeTab!=='undefined') ? activeTab : null
    }));
  }catch(e){}
}

function renderList(){
  // until the first inventory snapshot lands (the daemon's initial docker/
  // virsh scans take a moment), show an honest "looking for sandboxes" spinner
  if(!state.firstData){
    $('cnt-c').textContent=''; $('cnt-vl').textContent=''; $('cnt-vd').textContent='';
    var spin='<div class="fspin"><span class="spin"></span><span class="fspin-msg">looking for sandboxes…</span></div>';
    $('items-c').innerHTML=spin; $('items-vl').innerHTML=spin; $('items-vd').innerHTML=spin;
    return;
  }
  var groups={'items-c':[], 'items-vl':[], 'items-vd':[]};
  state.list.forEach(function(sb){ var g=groupFor(sb); if(g) groups[g].push(sb); });
  Object.keys(groups).forEach(function(gid){
    var host=$(gid); host.innerHTML='';
    groups[gid].forEach(function(sb){ host.appendChild(renderItem(sb)); });
    var cnt=groups[gid].length;
    var map={ 'items-c':'cnt-c','items-vl':'cnt-vl','items-vd':'cnt-vd' };
    $(map[gid]).textContent=cnt? cnt : '';
  });
  if(state.sel && !state.list.some(function(sb){return keyOf(sb)===state.sel;})){
    state.sel=null; showEmpty();
  }
}
function showEmpty(){
  if(state.view!=='sandbox'){ return; } // global view owns the main area
  $('pane').hidden=true; $('empty').hidden=false;
}

/* token counts get thousands separators so "tok today" is readable */
function fmtTok(n){
  n=Math.round(Number(n)||0);
  if(!n) return '0';
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g,',');
}

/* ---------- global views (overview / burn / config) ---------- */
var hostTimer=null; // polling for the overview page; cleared on every view change
var burnTimer=null; // polling for the burn ledger page; same contract
var burnShowStale=false; // show-stale toggle state (undeclared ghost fixed: reading it in render guards threw and blanked the whole burn view)
function clearTimers(){
  if(hostTimer){ clearInterval(hostTimer); hostTimer=null; }
  if(burnTimer){ clearInterval(burnTimer); burnTimer=null; }
  if(dnsTimer){ clearInterval(dnsTimer); dnsTimer=null; }
}
function showGlobal(kind){
  clearTimers();
  state.view=kind; state.sel=null; closeLogs();
  renderList();
  $('pane').hidden=true; $('empty').hidden=true; $('global').hidden=false;
  var titles={overview:'Overview - host & sandboxes', burn:'Burn - token & dollar ledger', create:'Create sandbox', config:'Config - global OpenCode settings', help:'Help - quick start & CLI reference'};
  var subs={
    overview:'Machine stats and every sandbox on this host. Updates every 5 seconds.',
    burn:'Proxy traffic per sandbox and in total. Provider keys stay on the host.',
    create:'Runs through the daemon. Mounts and ports are fixed at creation.',
    config:'Providers, proxy and prices. Stored in ~/.config/hotbed/config.json',
    help:'The three CLIs (hbd, hotbedvm, hotbedd), the burn ledger mechanics and troubleshooting tips.'
  };
  $('glob-title').textContent = titles[kind] || 'Config - global OpenCode settings';
  $('glob-sub').textContent = subs[kind] || subs.config;
  $('glob-actions').innerHTML='';
  $('glob-body').innerHTML='';
  if(kind==='burn'){ renderBurnGlobal(); }
  else if(kind==='create'){ renderCreator(); }
  else if(kind==='overview'){ renderHostOverview(); }
  else if(kind==='help'){ renderHelp(); }
  else { renderConfigGlobal(); }
  saveView();
}
function kindFor(name){
  var sb=state.list.filter(function(x){return x.name===name;})[0];
  return sb ? sb.kind : 'vm';
}

/* ---------- overview: host metrics + inventory counts (POST /api/stats) ---------- */
function humanBytes(n){
  var u=['B','KiB','MiB','GiB','TiB'], i=0;
  n=Number(n)||0;
  while(n>=1024 && i<u.length-1){ n/=1024; i++; }
  return (i===0 ? n : n.toFixed(1))+' '+u[i];
}
function humanDur(s){
  s=Math.floor(Number(s)||0);
  var d=Math.floor(s/86400), h=Math.floor(s%86400/3600), m=Math.floor(s%3600/60);
  if(d>0) return d+'d '+h+'h';
  if(h>0) return h+'h '+m+'m';
  if(m>0) return m+'m';
  return s+'s';
}
function ovBar(p){
  p=Number(p)||0;
  var cls=p>=85?'hot':p>=60?'warn':'';
  return '<div class="ov-bar"><span class="'+cls+'" style="width:'+Math.min(100,p)+'%"></span></div>';
}
function ovKV(k,v){
  return '<div class="ov-kv"><span>'+esc(k)+'</span><span>'+v+'</span></div>';
}
function renderHostOverview(){
  var body=$('glob-body');
  function paint(j){
    if(state.view!=='overview') return; // a view switch happened mid-flight
    if(j.error){ body.innerHTML='<div class="hint"><span class="err">'+esc(j.error)+'</span></div>'; return; }
    var c=j.cpu||{}, m=j.mem||{}, discs=j.disk||[], hs=j.host||{}, hbd=j.sandboxes||{};
    var cpuCard='<div class="ov-card"><div class="ov-h">CPU <b>'+(Number(c.percent)||0).toFixed(1)+'%</b></div>'+ovBar(c.percent)+
      '<div style="margin-top:var(--s3)">'+
      ovKV('vCPUs', esc(c.cores!=null?c.cores:'–'))+
      ovKV('load 1 / 5 / 15 min', esc([c.load1,c.load5,c.load15].map(function(x){return x!=null?x:'–';}).join(' / ')))+
      ovKV('model', esc(hs.cpu_model||'unknown'))+
      '</div></div>';
    var memCard='<div class="ov-card"><div class="ov-h">Memory <b>'+(Number(m.percent)||0).toFixed(1)+'%</b></div>'+ovBar(m.percent)+
      '<div style="margin-top:var(--s3)">'+
      ovKV('total', esc(humanBytes(m.total)))+
      ovKV('used', esc(humanBytes(m.used)))+
      ovKV('available', esc(humanBytes(m.available)))+
      ovKV('swap', esc(humanBytes(m.swap_used)+' of '+humanBytes(m.swap_total)))+
      '</div></div>';
    function grp(t,g){ return ovKV(t, esc(((g||{}).running!=null?g.running:'–')+' running · '+((g||{}).total!=null?g.total:'–')+' known')); }
    var hbCard='<div class="ov-card"><div class="ov-h">Sandboxes <b>'+(hbd.containers?hbd.containers.total:0)+(hbd.vms_libvirt?hbd.vms_libvirt.total:0)+(hbd.vms_docker?hbd.vms_docker.total:0)+'</b></div>'+
      '<div style="margin-top:var(--s3)">'+
      grp('containers (hbd)',hbd.containers)+
      grp('VMs · libvirt (hotbedvm)',hbd.vms_libvirt)+
      grp('VMs · docker (hotbedvmd)',hbd.vms_docker)+
      '</div>'+
      '<div style="margin-top:var(--s3)">'+
      '<span class="hbk'+((state.health||{}).docker!==false?' on':' off')+'">docker</span>'+
      '<span class="hbk'+((state.health||{}).libvirt!==false?' on':' off')+'">libvirt</span>'+
      '<span class="hbk'+((state.health||{}).tmux!==false?' on':' off')+'">tmux</span>'+
      '</div></div>';
    var sysCard='<div class="ov-card"><div class="ov-h">System <b>'+esc(humanDur(hs.uptime_seconds))+'</b></div>'+
      '<div style="margin-top:var(--s3)">'+
      ovKV('kernel', esc(hs.kernel||'unknown'))+
      ovKV('daemon', 'hotbedd '+esc(j.version||''))+
      ovKV('uptime', esc(humanDur(hs.uptime_seconds)))+
      '</div></div>';
    // one row of metric cards, then a slim disk strip, then per-sandbox cards
    var diskStrip=discs.length?'<div class="ov-disks">'+discs.map(function(d){
      return '<div class="ov-disk" title="'+esc(humanBytes(d.used))+' used · '+esc(humanBytes(d.available))+' free">'+
        '<span class="mnt">'+esc(d.mount==='/'?'root':d.mount)+'</span>'+ovBar(d.percent)+
        '<span class="pct">'+(Number(d.percent)||0).toFixed(0)+'%</span></div>';
    }).join('')+'</div>':'';
    body.innerHTML='<div class="ov-grid ov-row">'+cpuCard+memCard+hbCard+sysCard+'</div>'+
      diskStrip+
      '<div class="ov-h" style="margin:var(--s4) 0 0 0">Running sandboxes</div>'+
      '<div id="ov-hbs"></div>';
    paintOvSandboxes($('ov-hbs'));
  }
  body.innerHTML='<div class="hint" style="display:flex;align-items:center;gap:8px"><span class="spin"></span>reading host metrics…</div>';
  var tick=function(){
    if(state.view!=='overview') return;
    fetch('/api/stats').then(function(r){ return r.json(); }).then(paint)
      .catch(function(){ if(state.view==='overview') body.innerHTML='<div class="hint"><span class="err">stats fetch failed (daemon unreachable?)</span></div>'; });
  };
  tick();
  hostTimer=setInterval(tick,5000);
}
/* overview sub-section: one clickable card per sandbox, with live meters.
   Metric truth comes from the inventory snapshot (cpu/mem/state) plus the
   burn ledger (today's burn + call counts). */
function paintOvSandboxes(box){
  if(!box) return;
  Promise.all([
    fetch('/api/sandboxes').then(function(r){ return r.json(); }),
    fetch('/api/usage').then(function(r){ return r.json(); }).catch(function(){ return {}; })
  ]).then(function(res){
    if(state.view!=='overview'||!box.isConnected) return; // view switch mid-flight
    var list=res[0].sandboxes||[];
    var burn={};
    (res[1].rows||[]).forEach(function(r){ burn[r.sandbox]=r; });
    if(!list.length){
      box.innerHTML='<div class="hint">no sandboxes yet - create one with the + button in the sidebar</div>';
      return;
    }
    var cards=list.map(function(sb){
      var u=burn[sb.name]||{};
      var pills='<span class="pill '+esc((sb.state||'').replace(/[\s-]+/g,''))+'">'+esc(sb.state||'unknown')+'</span>'+
        '<span class="chipmuted">'+esc(sb.kind)+(sb.backend?' · '+esc(sb.backend):'')+'</span>';
      if(sb.cpuPercent!=null&&sb.cpuPercent>0.005) pills+='<span class="chipmuted">'+sb.cpuPercent.toFixed(1)+'% cpu</span>';
      if(sb.memUsage) pills+='<span class="chipmuted">'+esc(sb.memUsage)+' mem</span>';
      if(u.tokens_today) pills+='<span class="chipmuted">'+fmtTok(u.tokens_today)+' tok today</span>';
      if(u.calls) pills+='<span class="chipmuted">'+(u.calls||0)+' calls</span>';
      return '<div class="ov-card ovhotbed" data-kind="'+esc(sb.kind)+'" data-name="'+esc(sb.name)+'" title="open '+esc(sb.name)+'">'+
        '<div class="ov-h"><b>'+esc(sb.name)+'</b>'+
        (u.today_usd? ' <b style="color:var(--acc)">$'+(Number(u.today_usd)||0).toFixed(4)+'</b>' : '')+
        '</div><div style="margin-top:var(--s2)">'+pills+'</div></div>';
    });
    box.innerHTML='<div class="ov-grid">'+cards.join('')+'</div>';
    Array.prototype.forEach.call(box.querySelectorAll('.ovhotbed'), function(card){
      card.addEventListener('click', function(){ openFromOverview(card.dataset.kind, card.dataset.name); });
    });
  }).catch(function(){ /* overview repaints every 5s; a failed round trip is not worth a toast */ });
}
function openFromOverview(kind, name){
  function find(sbList){
    return sbList.filter(function(x){ return x.name===name && x.kind===kind; })[0];
  }
  var sb=find(state.list);
  if(sb){ select(sb); return; }
  fetch('/api/sandboxes').then(function(r){ return r.json(); }).then(function(d){
    var sb2=find(d.sandboxes||[]);
    if(sb2){ select(sb2); } else { toast(name+' is not in the inventory (just deleted?)','err'); }
  }).catch(function(){ toast('could not open '+name,'err'); });
}

/* Burn fetches fail transiently when the daemon restarts behind an open
   dashboard - do not dead-end the tab: keep the message honest and offer
   one retry button that simply re-renders. */
/* ---------- in-site modal: confirm-style dialogs instead of window.confirm ---------- */
/* askConfirm({title, body(html, esc-able), confirmLabel, danger, attrs[](html)}
   → calls cb(ok). Cancel/✕/Esc = false; only the confirm button = true. */
function askConfirm(opts, cb){
  ensureAsk();
  $('ask-title').textContent=opts.title||'Are you sure?';
  $('ask-body-body').innerHTML=opts.body||'';
  $('ask-attr').innerHTML=(opts.attrs||[]).map(function(a){ return '<div class="ask-att">'+a+'</div>'; }).join('');
  var btn=$('ask-ok');
  btn.textContent=opts.confirmLabel||'Confirm';
  btn.classList.toggle('ask-danger', !!opts.danger);
  if(!opts.danger){ btn.classList.add('primary'); } else { btn.classList.remove('primary'); }
  var modal=$('ask-modal');
  var close=function(ok){
    modal.classList.remove('open');
    modal.onclick=null;
    document.removeEventListener('keydown', askEsc);
    function noop(){}
    (opts.clean||noop)();
    if(typeof cb==='function'){ cb(!!ok); }
  };
  function askEsc(ev){ if(ev.key==='Escape'){ close(false); } }
  $('ask-cancel').onclick=function(){ close(false); };
  $('ask-x').onclick=function(){ close(false); };
  btn.onclick=function(){ close(true); };
  modal.onclick=function(ev){ if(ev.target===modal){ close(false); } };
  document.addEventListener('keydown', askEsc);
  modal.classList.add('open');
  btn.focus();
}
function ensureAsk(){
  if($('ask-modal')){ return; }
  var m=h('div',{id:'ask-modal'},
    '<div class="ask-card">'+
    '<div class="ask-head"><div class="ask-title" id="ask-title"></div><button type="button" class="iconbtn" id="ask-x" title="close">✕</button></div>'+
    '<div class="ask-body"><div id="ask-body-body"></div><div id="ask-attr"></div></div>'+
    '<div class="ask-foot"><button type="button" id="ask-cancel">Cancel</button><button type="button" id="ask-ok">Confirm</button></div>'+
    '</div>');
  document.body.appendChild(m);
}

function burnRetry(host, rerender){
  host.innerHTML='<div class="hint">burn data unavailable (daemon unreachable or restarting?) '+
    '<button class="tool" id="burn-retry">retry</button></div>';
  var b=$('burn-retry');
  if(b){ b.addEventListener('click', rerender); }
}

function addTool(bar,label,fn){
  var b=h('button',{class:'tool'},esc(label));
  b.addEventListener('click',fn); bar.appendChild(b); return b;
}

var lastBurnKeys=''; // structural signature of the burn table; value changes do NOT rebuild
function renderBurnGlobal(){
  var body=$('glob-body');
  // burn rows change as sandboxes dial the proxy - poll while the tab is
  // open (skipping ticks whenever the user works an input or an open
  // drill so neither focus nor open state is clobbered by a re-render).
  if(burnTimer){ clearInterval(burnTimer); burnTimer=null; }
  var finish=function(){
    burnTimer=setInterval(function(){
      var ae=document.activeElement;
      if(state.view!=='burn') { clearInterval(burnTimer); burnTimer=null; return; }
      if(ae && (ae.tagName==='INPUT'||ae.tagName==='TEXTAREA'||ae.tagName==='SELECT')) return;
      if(body.querySelector('.drillrow:not([hidden])')) return;
      renderBurnGlobal();
    },5000);
  };
  // a live table stays visible during the fetch - only a first paint shows
  // loading, so every 5s tick updates cells in place instead of flickering
  if(!body.querySelector('#burntbl')){ body.innerHTML='<div class="hint">loading…</div>'; }
  fetch('/api/usage').then(function(r){return r.json();}).then(function(j){
    if(state.view!=='burn') return; // a view switch happened mid-flight
    paintBurn(j.rows||[], body);
    finish();
  }).catch(function(){ lastBurnKeys=''; burnRetry(body, renderBurnGlobal); });
}
function burnChip(stale){
  return stale
    ?'<span class="chipmuted">stale</span>'
    :'<span class="chipmuted" style="color:var(--grn);border-color:rgba(76,195,138,.5)">live</span>';
}
function paintBurn(rows, body){
  if(!rows.length){ lastBurnKeys=''; body.innerHTML='<div class="hint">No usage yet. Flow: hotbedd config init → hotbedd sync NAME → sandbox calls go through the proxy → rows appear here.</div>'; return; }
  var todaySum=0,allSum=0,live=0,staleUnseen=0,staleTotal=0;
  rows.forEach(function(r){
    todaySum+=r.today_usd||0; allSum+=r.total_usd||0;
    if(r.exists){ live++; } else { staleTotal++; if(!r.pinned) staleUnseen++; }
  });
  // STRUCTURE of the table: which rows exist, pinned flags, the unpinned
  // stale count (purge label) and the show-stale toggle. If it did not
  // change, only cell values are patched in place (no flicker, no focus
  // loss); anything structural falls back to a full rebuild.
  var keys=rows.map(function(r){ return [r.sandbox,!!r.exists,!!r.pinned].join(':'); }).join('|')+'#'+staleUnseen+'*'+(burnShowStale?1:0);
  if(lastBurnKeys && keys===lastBurnKeys && body.querySelector('#burntbl') && body.querySelector('.burnbar')){
    patchBurn(rows, body, todaySum, allSum, live, staleUnseen);
    return;
  }
  lastBurnKeys=keys;
  var bar='<div class="burnbar">'+
    '<div class="bstat"><div class="v" id="bb-today">$'+todaySum.toFixed(4)+'</div><div class="k">burn today</div></div>'+
    '<div class="bstat"><div class="v" id="bb-all">$'+allSum.toFixed(4)+'</div><div class="k">burn all-time</div></div>'+
    '<div class="bstat"><div class="v" id="bb-live">'+live+'</div><div class="k">live sandboxes</div></div>'+
    '<div class="bstat" id="bb-stalecard"><div class="v" id="bb-stale">'+staleUnseen+'</div><div class="k">stale history rows</div></div>'+
    '</div>'+
    '<div class="burntools">'+
      '<label><span class="ios-toggle"><input type="checkbox" id="burn-show-stale"'+(burnShowStale?' checked':'')+'><span class="track"></span><span class="knob"></span></span>show stale rows</label>'+
      '<button class="tool" id="burn-purge">🗑 purge stale history'+(staleUnseen?' ('+staleUnseen+')':'')+'</button>'+
      '<span class="hint">stale = sandbox deleted · ★ pinned histories stay visible and survive the bulk purge</span>'+
    '</div>';
  var tbl='<table class="tbl" id="burntbl"><thead><tr><th></th><th>sandbox</th><th>state</th><th>today $</th><th>total $</th><th>calls</th><th>tok today</th><th>budget $</th><th>budget tok</th><th></th></tr></thead><tbody>';
  rows.forEach(function(r){
    var stale=!r.exists;
    var hidden=stale&&!r.pinned&&!burnShowStale;
    var dim=stale?' class="rowdim"':'';
    var pin='<button class="starpin'+(r.pinned?' on':'')+' tpin" data-hbd="'+esc(r.sandbox)+'" data-p="'+(r.pinned?1:0)+'" title="'+(r.pinned?'unpin':'pin burn history')+'">'+(r.pinned?'★':'☆')+'</button>';
    var bud;
    if(stale){
      bud='<td>-</td><td>-</td>';
    }else{
      bud='<td><input class="money" data-hbd="'+esc(r.sandbox)+'" value="'+(r.budget_usd>0? r.budget_usd : '')+'"></td>'+
          '<td><input class="money" data-tok="'+esc(r.sandbox)+'" value="'+(r.budget_tokens>0? r.budget_tokens : '')+'"></td>';
    }
    var acts;
    if(stale){
      acts='<button class="tool trowdel" data-hbd="'+esc(r.sandbox)+'">delete records</button>';
    }else{
      acts='<button class="tool tbud" data-hbd="'+esc(r.sandbox)+'">set</button>'+
           '<button class="tool tdrill" data-hbd="'+esc(r.sandbox)+'">detail</button>'+
           '<button class="tool tsync" data-hbd="'+esc(r.sandbox)+'">sync</button>';
    }
    tbl+='<tr'+dim+(hidden?' data-hidden="1" hidden':'')+' data-hbd="'+esc(r.sandbox)+'"><td>'+pin+'</td><td>'+esc(r.sandbox)+'&nbsp;&nbsp;'+burnChip(stale)+'</td>'+
      '<td>$'+(r.today_usd||0).toFixed(4)+'</td><td>$'+(r.total_usd||0).toFixed(4)+'</td><td>'+(r.calls||0)+'</td><td>'+fmtTok(r.tokens_today)+'</td>'+
      bud+'<td class="actions">'+acts+'</td></tr>'+
      (stale?'':'<tr class="drillrow" data-hbd="'+esc(r.sandbox)+'" hidden><td colspan="10" id="drill-'+esc(r.sandbox)+'"></td></tr>');
  });
  tbl+='</tbody></table>';
  body.innerHTML=bar+tbl;
  var chk=body.querySelector('#burn-show-stale');
  if(chk){ chk.addEventListener('change',function(){ burnShowStale=chk.checked; renderBurnGlobal(); }); }
  var pp=body.querySelector('#burn-purge');
  if(pp){
    pp.disabled=staleUnseen===0;
    if(staleUnseen>0){
      pp.addEventListener('click',function(){
        askConfirm({
          title:'Purge stale burn history?',
          body:'Delete the burn history of <b>'+staleUnseen+'</b> stale (unpinned) sandbox row(s)? Pinned <b>★</b> histories are kept.',
          confirmLabel:'Purge '+staleUnseen,
          danger:true,
          attrs:['Permanently removes their journal rows from <span class="mono">usage.jsonl</span>. This cannot be undone.']
        },function(ok){
          if(!ok){ return; }
          askConfirm({
            title:'Second confirm',
            body:'This <b>permanently</b> deletes the stale history. Proceed?',
            confirmLabel:'Delete forever',
            danger:true
          },function(ok2){
            if(!ok2){ return; }
            fetch('/api/usage/purge',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({stale_only:true})})
              .then(function(r){return r.json();}).then(function(res){
                if(res.error){ toast('purge: '+res.error,'err'); }
                else { toast('purged '+(res.purged||[]).length+' stale record set(s), '+(res.rows||0)+' rows dropped','ok'); lastBurnKeys=''; renderBurnGlobal(); }
              });
          });
        });
      });
    }
  }
  Array.prototype.forEach.call(body.querySelectorAll('.tpin'),function(b){
    b.addEventListener('click',function(){
      var name=b.getAttribute('data-hbd'); var to=b.getAttribute('data-p')==='1'?false:true;
      fetch('/api/usage/pin',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,pinned:to})})
        .then(function(r){return r.json();}).then(function(res){
          if(res.error){ toast('pin: '+res.error,'err'); }
          else { toast((res.pinned?'pinned ':'unpinned ')+name,'ok'); lastBurnKeys=''; renderBurnGlobal(); }
        });
    });
  });
  Array.prototype.forEach.call(body.querySelectorAll('.trowdel'),function(b){
    b.addEventListener('click',function(){
      var name=b.getAttribute('data-hbd');
      askConfirm({
        title:'Delete burn records?',
        body:'Delete the burn records of <span class="mono">'+esc(name)+'</span>?',
        confirmLabel:'Delete records',
        danger:true,
        attrs:['Its sandbox no longer exists. Removes the journal rows <b>and</b> its leftover wallet + sync target, so the row cannot come back.']
      },function(ok){
        if(!ok){ return; }
        fetch('/api/usage/purge',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sandboxes:[name]})})
          .then(function(r){return r.json();}).then(function(res){
            if(res.error){ toast('purge: '+res.error,'err'); }
            else { toast('deleted records for '+name+' ('+(res.rows||0)+' rows)','ok'); lastBurnKeys=''; renderBurnGlobal(); }
          });
      });
    });
  });
  Array.prototype.forEach.call(body.querySelectorAll('.tbud'),function(b){
    b.addEventListener('click',function(){
      var name=b.getAttribute('data-hbd');
      var inp=body.querySelector('input.money[data-hbd="'+name+'"]');
      var tok=body.querySelector('input.money[data-tok="'+name+'"]');
      var usd=(inp.value==='')?null:(parseFloat(inp.value)||0);
      var toks=(tok.value==='')?null:(parseInt(tok.value,10)||0);
      fetch('/api/usage/budget',{method:'POST',headers:{'Content-Type':'application/json'},
        body:JSON.stringify({name:name,usd:usd,tokens:toks})}).then(function(r){return r.json();})
        .then(function(res){ if(res.error) toast('budget: '+res.error,'err'); else toast('wallets set for '+name,'ok'); });
    });
  });
  Array.prototype.forEach.call(body.querySelectorAll('.tdrill'),function(b){
    b.addEventListener('click',function(){ drillToggle(b.getAttribute('data-hbd')); });
  });
  Array.prototype.forEach.call(body.querySelectorAll('.tsync'),function(b){
    b.addEventListener('click',function(){
      var name=b.getAttribute('data-hbd'); var kind=kindFor(name);
      fetch('/api/sandboxes/'+kind+'/'+encodeURIComponent(name)+'/sync',{
        method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})
        .then(function(r){return r.json();})
        .then(function(res){ if(res.error){ toast('sync: '+res.error,'err'); } else { toast(name+' synced to proxy ('+(res.target.key_masked||'')+')','ok'); } });
    });
  });
}
/* value-only burn tick: patch stat cards + row cells in place. */
function patchBurn(rows, body, todaySum, allSum, live, staleUnseen){
  var setv=function(id,v){ var el=body.querySelector('#'+id); if(el){ el.textContent=v; } };
  setv('bb-today','$'+todaySum.toFixed(4));
  setv('bb-all','$'+allSum.toFixed(4));
  setv('bb-live',String(live));
  setv('bb-stale',String(staleUnseen));
  var sc=body.querySelector('#bb-stalecard'); if(sc){ sc.classList.toggle('warn',staleUnseen>0); }
  Array.prototype.forEach.call(body.querySelectorAll('#burntbl tbody tr[data-hbd]:not(.drillrow)'),function(tr){
    var name=tr.getAttribute('data-hbd');
    var r=null;
    for(var i=0;i<rows.length;i++){ if(rows[i].sandbox===name){ r=rows[i]; break; } }
    if(!r){ return; }
    var stale=!r.exists;
    tr.classList.toggle('rowdim',stale);
    var cells=(stale || tr.querySelectorAll('td').length>=9) ? tr.querySelectorAll('td') : null;
    if(!cells || cells.length<9){ return; }
    cells[2].textContent='$'+(r.today_usd||0).toFixed(4);
    cells[3].textContent='$'+(r.total_usd||0).toFixed(4);
    cells[4].textContent=String(r.calls||0);
    cells[5].textContent=fmtTok(r.tokens_today);
    if(stale){ return; }
    var u=tr.querySelector('input.money[data-hbd]');
    var t=tr.querySelector('input.money[data-tok]');
    var uv=(r.budget_usd>0? String(r.budget_usd) : '');
    var tv=(r.budget_tokens>0? String(r.budget_tokens) : '');
    if(u && u.value!==uv){ u.value=uv; }
    if(t && t.value!==tv){ t.value=tv; }
  });
}


/* ---------- create wizard (POST /api/create) ---------- */
var icoDir='<svg class="fi" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M1.5 3.2C1.5 2.5 2 2 2.7 2h3.2c.4 0 .8.2 1 .5l.9 1h5.5c.7 0 1.2.5 1.2 1.2v7.1c0 .7-.5 1.2-1.2 1.2H2.7c-.7 0-1.2-.5-1.2-1.2V3.2Z"/></svg>';
var icoFile='<svg class="fi" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M3.4 1.1h5.2l3.9 3.9v9.4c0 .5-.4.9-.9.9H3.4c-.5 0-.9-.4-.9-.9V2c0-.5.4-.9.9-.9Z"/></svg>';

function renderCreator(){
  var body=$('glob-body');
  var stackOpts=['go','node','python','rust','base'].map(function(s){return '<option'+(s==='go'?' selected':'')+'>'+s+'</option>';}).join('');
  body.innerHTML=
    '<div class="hint">Containers run on this host\'s Docker with the kit\'s labels, mounts and auth setup. <b>VMs</b> boot a full Debian guest (libvirt or Docker-backed QEMU): creation returns at spawn and the guest finishes booting in the background - models arrive automatically once the guest is up. First VM on this host needs the master image once (<code>hotbedvm fetch-image</code>).</div>'+
    '<div class="cw">'+
    '<div class="cw-card">'+
    '<div class="cw-sec"><div class="cw-sec-t">Sandbox kind</div>'+
      '<div class="cw-kind">'+
        '<label class="check"><input type="radio" name="cwkind" value="container" checked><span class="box"></span> container (hbd)</label>'+
        '<label class="check"><input type="radio" name="cwkind" value="vm-libvirt"><span class="box"></span> VM · libvirt (hotbedvm)</label>'+
        '<label class="check"><input type="radio" name="cwkind" value="vm-docker"><span class="box"></span> VM · docker (hotbedvmd)</label>'+
      '</div>'+
      '<div class="fm" id="cw-kindmsg">Same kernel as the host. Spawns in seconds; mounts and ports are fixed at creation.</div>'+
    '</div>'+
    '<div class="cw-sec"><div class="cw-sec-t">Basics</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-name">Name</label><input id="cw-name" placeholder="myproj" autocomplete="off" spellcheck="false" maxlength="40"><div class="fm" id="cw-namemsg"></div></div>'+
      '<div class="field" id="cw-stack-field"><label for="cw-stack">Stack image</label><select id="cw-stack">'+stackOpts+'</select><div class="fm">needs the kit image - hbd build &lt;stack&gt; builds it once</div></div>'+
    '</div></div>'+
    '<div id="cw-cont-fields">'+
    '<div class="cw-sec"><div class="cw-sec-t">Network</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-egress">Egress</label><select id="cw-egress"><option>allow</option><option>allowlist</option><option>proxy-only</option><option>none</option></select><div class="fm">outbound fence; re-apply after recreates</div></div>'+
      '<div class="field" id="cw-egdom-row" style="display:none"><label for="cw-eg-dom">Allowed domains</label><input id="cw-eg-dom" style="width:100%" placeholder="api.openai.com, example.org" autocomplete="off" title="opencode.ai is needed to converse with models: opencode authenticates and fetches its model list from api.opencode.ai, and this entry already covers it via suffix match; without it, opencode fails early (EAI_AGAIN) instead of chatting. Update checks also phone home there."><div class="fm">comma/space separated; suffix match covers subdomains; DNS goes through the fence, everything else drops. May stay empty with tracking on: allow what the DNS tab shows later. Include opencode.ai: synchronized opencode uses it for login and the model list, so without it chatting fails</div></div>'+
      '<div class="field" id="cw-egtrack-row"><label>DNS tracking</label><label class="check"><input type="checkbox" id="cw-eg-track" checked><span class="box"></span>record lookups</label><div class="fm">see every domain the sandbox resolves in its DNS tab, allow forgotten ones there afterwards</div></div>'+
    '</div>'+
    '<div class="rows-t">Published ports - host → container</div>'+
    '<div class="rows" id="cw-ports"></div>'+
    '<button type="button" class="addrow" id="cw-addport">＋ add port</button></div>'+
    '<div class="cw-sec"><div class="cw-sec-t">Environment</div>'+
    '<div class="rows" id="cw-envrows"></div>'+
    '<button type="button" class="addrow" id="cw-addenv">＋ add variable</button>'+
    '<div class="fm">key → value pairs passed to the container at start</div></div>'+
    '<div class="cw-sec"><div class="cw-sec-t">Mounts</div>'+
    '<div class="rows" id="cw-mounts"></div>'+
    '<button type="button" class="addrow" id="cw-addmount">＋ add mount</button>'+
    '<div class="fm">host folders shared into the sandbox (read-only recommended)</div></div>'+
    '</div>'+  /* end container-only fields */
    '<div id="cw-vm-fields" hidden>'+
    '<div class="cw-sec"><div class="cw-sec-t">VM sizing</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-vmcpu">vCPUs</label><select id="cw-vmcpu"><option>1</option><option selected>2</option><option>4</option><option>8</option></select><div class="fm">1–16</div></div>'+
      '<div class="field"><label for="cw-vmmem">Memory (MB)</label><select id="cw-vmmem"><option>2048</option><option selected>4096</option><option>6144</option><option>8192</option></select><div class="fm">512–65536</div></div>'+
      '<div class="field"><label for="cw-vmdisk">Disk (GB)</label><input id="cw-vmdisk" class="money big" value="20" inputmode="numeric"><div class="fm">growable qcow2, 10–512</div></div>'+
      '<div class="field"><label for="cw-vmshare">Share directory (host)</label><div class="mwrap"><input id="cw-vmshare" placeholder="optional - e.g. ~/projects/myapp" spellcheck="false"><button type="button" class="browse" id="cw-vmshare-browse">Browse…</button></div><div class="fm">folder-picker as on container mounts - mounted at /workspace in the guest, empty = VM-local workspace</div></div>'+
    '</div>'+
    '<div class="fm">The guest logs in with your uid; opencode talks to the host proxy. Models arrive automatically at boot end (fallback: the sync button under Burn).</div>'+
    '</div>'+
    '<div class="cw-sec"><div class="cw-sec-t">OpenCode</div><div class="cw-grid two">'+
      '<div class="field"><label>Sync to hbd proxy</label><label class="check"><input type="checkbox" id="cw-sync" checked><span class="box"></span>mint sandbox key &amp; write transport</label><div class="fm">provider keys never enter the sandbox</div></div>'+
      '<div class="field"><label for="cw-provider">Provider</label><input id="cw-provider" placeholder="default provider" spellcheck="false"><div class="fm">empty = the default provider in config.json</div></div>'+
    '</div></div>'+
    '<div class="cw-sec"><div class="cw-sec-t">Budgets</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-usd">USD budget</label><input id="cw-usd" class="money big" placeholder="unlimited" inputmode="decimal"><div class="fm">empty = unlimited</div></div>'+
      '<div class="field"><label for="cw-tok">Token wallet</label><input id="cw-tok" class="money big" placeholder="unlimited" inputmode="numeric"><div class="fm">empty = unlimited</div></div>'+
    '</div></div>'+
    '</div>'+
    '<div class="cw-bar"><button type="button" class="primary" id="cw-go">Create sandbox</button><span class="cw-msg" id="cw-msg"></span></div>'+
    '</div>';
  portRow($('cw-ports'));
  envRow($('cw-envrows'));
  mountRow($('cw-mounts'));
  $('cw-addport').addEventListener('click',function(){ portRow($('cw-ports')); });
  $('cw-addenv').addEventListener('click',function(){ envRow($('cw-envrows')); });
  $('cw-addmount').addEventListener('click',function(){ mountRow($('cw-mounts')); });
  // egress allowlist: the domains input exists only while "allowlist" is
  // selected — it keeps its value (documented) when the preset switches
  // away and back, but is only applied on create when allowlist is active.
  var egressDomUpdate=function(){
    var v=$('cw-egress').value;
    $('cw-egdom-row').style.display=(v==='allowlist')?'':'none';
    // tracking needs DNS through the fence: allow and allowlist only
    $('cw-egtrack-row').style.display=(v==='allow'||v==='allowlist')?'':'none';
  };
  $('cw-egress').addEventListener('change',egressDomUpdate);
  egressDomUpdate();
  // VM shares use the same folder browser as container mounts - one picker,
  // all three sandbox kinds (libvirt + docker-backed VMs share cw-vmshare).
  $('cw-vmshare-browse').addEventListener('click',function(){
    openFS(function(p){ $('cw-vmshare').value=p; });
  });
  nameDupCheck();
  Array.prototype.forEach.call(document.querySelectorAll('input[name="cwkind"]'),function(rd){
    rd.addEventListener('change',updateCreatorKind);
  });
  fetch('/api/health').then(function(r){return r.json();}).then(function(h){
    if(h && h.libvirt===false){
      var rb=document.querySelector('input[name="cwkind"][value="vm-docker"]');
      if(rb && !document.querySelector('input[name="cwkind"]:checked[value^="vm"]')){ rb.checked=true; updateCreatorKind(); }
    }
  }).catch(function(){ console.warn('dashboard: request failed silently in ?'); });
  $('cw-go').addEventListener('click',function(){
    var name=($('cw-name').value||'').trim().toLowerCase();
    var msg=$('cw-msg');
    msg.textContent='';
    if(!name){ msg.textContent='name required'; return; }
    // busy guard + spinner: while an in-flight create is running the button
    // is disabled (and pointer-events killed via .busy) - a second press
    // cannot fire a second POST /api/create for any of the three kinds.
    var btn=$('cw-go');
    if(btn.disabled) return;
    function cwBusy(t){ btn.disabled=true; btn.classList.add('busy'); btn.textContent=t; }
    function cwIdle(){ btn.disabled=false; btn.classList.remove('busy'); btn.textContent='Create sandbox'; }
    var kindSel=(document.querySelector('input[name="cwkind"]:checked')||{}).value||'container';
    if(kindSel!=='container'){
      cwBusy(kindSel==='vm-libvirt'?'Spawning VM…':'Spawning hotbedvmd…');
      msg.textContent='provisioning VM - returns the moment the machine spawns; the guest finishes booting in the background…';
      var payload={
        kind:'vm', name:name,
        vm_backend: kindSel==='vm-libvirt'?'libvirt':'docker',
        vm_cpu: parseInt($('cw-vmcpu').value,10)||2,
        vm_mem_mb: parseInt($('cw-vmmem').value,10)||4096,
        vm_disk_gb: parseInt($('cw-vmdisk').value,10)||20,
        vm_share: ($('cw-vmshare').value||'').trim()
      };
      if($('cw-usd').value!==''){ payload.budget_usd=parseFloat($('cw-usd').value)||0; }
      if($('cw-tok').value!==''){ payload.budget_tokens=parseInt($('cw-tok').value,10)||0; }
      fetch('/api/create',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
        .then(function(r){return r.json();})
        .then(function(res){
          cwIdle();
          if(res.error){ msg.innerHTML='<span class="err">'+esc('create: '+res.error)+'</span>'; toast('create: '+res.error,'err'); return; }
          var w=res.warn?' (warnings: '+res.warn+')':'';
          toast('created VM '+res.vm,'ok');
          jumpToCreated('vm', res.vm, 'created VM '+res.vm+' ('+res.backend+' backend)'+w);
        })
        .catch(function(){ cwIdle(); msg.textContent='create failed (network) - the VM may still be provisioned by an earlier attempt; check hotbedd ps'; });
      return;
    }
    var ports=[], env={}, mounts=[], bad=[];
    Array.prototype.forEach.call(document.querySelectorAll('#cw-ports .drow'),function(row){
      var ph=row.querySelector('.phost').value.trim(), pc=row.querySelector('.pct').value.trim();
      if(ph&&pc){ ports.push(ph+':'+pc); } else if(ph||pc){ bad.push('ports'); }
    });
    Array.prototype.forEach.call(document.querySelectorAll('#cw-envrows .drow'),function(row){
      var k=row.querySelector('.ekey').value.trim(), v=row.querySelector('.eval').value.trim();
      if(k){ env[k]=v; } else if(v){ bad.push('environment'); }
    });
    Array.prototype.forEach.call(document.querySelectorAll('#cw-mounts .drow'),function(row){
      var s=row.querySelector('.msrc').value.trim(), t=row.querySelector('.mtgt').value.trim();
      if(s&&t){ mounts.push({source:s,target:t,mode:row.querySelector('.mro').checked?'ro':''}); }
      else if(s||t){ bad.push('mounts'); }
    });
    if(bad.length){
      bad=bad.filter(function(x,i,a){ return a.indexOf(x)===i; });
      msg.textContent='finish or clear the half-filled '+bad.join(' / ')+' row(s)';
      return;
    }
    var payload={
      name:name, stack:$('cw-stack').value, mounts:mounts, ports:ports,
      env:env, egress:$('cw-egress').value,
      sync:$('cw-sync').checked, provider:($('cw-provider').value||'').trim()
    };
    if(payload.egress==='allowlist'){
      payload.egress_domains=($('cw-eg-dom').value||'').split(/[\s,]+/).filter(Boolean);
    }
    if(payload.egress==='allow'||payload.egress==='allowlist'){
      payload.egress_track=$('cw-eg-track').checked;
    }
    if($('cw-usd').value!==''){ payload.budget_usd=parseFloat($('cw-usd').value)||0; }
    if($('cw-tok').value!==''){ payload.budget_tokens=parseInt($('cw-tok').value,10)||0; }
    cwBusy('Creating…');
    msg.textContent='creating… (the first create per stack builds its kit image - cached afterwards)';
    fetch('/api/create',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
      .then(function(r){return r.json();})
      .then(function(res){
        cwIdle();
        if(res.error){ msg.innerHTML='<span class="err">'+esc('create: '+res.error)+'</span>'; toast('create: '+res.error,'err'); return; }
        var w=res.warn?' (warnings: '+res.warn+')':'';
        toast('created '+res.container,'ok');
        jumpToCreated('container', res.container, 'created '+res.container+w);
      })
      .catch(function(){ cwIdle(); msg.textContent='create failed (network)'; });
  });
}
function updateCreatorKind(){
  var k=((document.querySelector('input[name="cwkind"]:checked')||{}).value)||'container';
  var isVM=k.indexOf('vm-')===0;
  var cf=document.getElementById('cw-cont-fields');
  var vf=document.getElementById('cw-vm-fields');
  var sf=document.getElementById('cw-stack-field');
  if(cf){ cf.hidden=isVM; }
  if(vf){ vf.hidden=!isVM; }
  if(sf){ sf.hidden=isVM; }
  var km=document.getElementById('cw-kindmsg');
  var nm=document.getElementById('cw-namemsg');
  if(km){
    km.textContent = k==='container' ? 'Same kernel as the host. Spawns in seconds; mounts and ports are fixed at creation.' :
      k==='vm-libvirt' ? 'libvirt VM on the host (hotbedvm-<name>) - needs qemu:///system; strongest isolation.' :
      'docker-backend VM (hotbedvmd-<name>) - QEMU in a container; works where libvirt is unavailable (e.g. Docker Desktop).';
  }
  if(nm){ nm.textContent=''; }
}
function nameDupCheck(){
  var inp=$('cw-name');
  if(!inp){ return; }
  inp.addEventListener('input',function(){
    var n=(this.value||'').trim().toLowerCase();
    var m=$('cw-namemsg');
    var k=((document.querySelector('input[name="cwkind"]:checked')||{}).value)||'container';
    var dup=n && state.list.some(function(sb){ return sb.name===n || (k==='container' && sb.name==='hotbed-'+n); });
    m.textContent=dup ? '"'+n+'" already exists - pick another name' :
      (k==='container' ? 'container becomes hotbed-<name> - a–z, 0–9, -' : 'VM keeps this name (hotbedvm-<name> / hotbedvmd-<name>) - letters, digits, . _ -');
    m.className='fm'+(dup?' iserr':'');
  });
}
function drow(host, inner){
  var row=h('div',{class:'drow'},inner+'<button type="button" class="iconbtn" title="remove" tabindex="-1">✕</button>');
  row.querySelector('.iconbtn').addEventListener('click',function(){ row.remove(); });
  host.appendChild(row);
  return row;
}
function portRow(host){
  drow(host,
    '<input class="phost" placeholder="host" inputmode="numeric" maxlength="5" spellcheck="false">'+
    '<span class="dsep">→</span>'+
    '<input class="pct" placeholder="container" inputmode="numeric" maxlength="5" spellcheck="false">');
}
function envRow(host){
  drow(host,
    '<input class="ekey" placeholder="KEY" spellcheck="false" autocomplete="off">'+
    '<span class="dsep">=</span>'+
    '<input class="eval" placeholder="value" spellcheck="false" autocomplete="off">');
}
function mountRow(host){
  var row=drow(host,
    '<div class="mwrap"><input class="msrc" placeholder="/home/…  folder on this machine" spellcheck="false">'+
    '<button type="button" class="browse">Browse…</button></div>'+
    '<span class="dsep">→</span>'+
    '<input class="mtgt" placeholder="/workspace/…  inside sandbox" spellcheck="false">'+
    '<label class="ro"><input type="checkbox" class="mro"><span class="box"></span>read-only</label>');
  row.querySelector('.browse').addEventListener('click',function(){
    openFS(function(p){ row.querySelector('.msrc').value=p; });
  });
  return row;
}

/* ---------- folder browser modal (GET /api/fs) ---------- */
var fsState={cb:null,path:'',home:'',sel:'',parent:''};
function openFS(cb){
  fsState.cb=cb;
  ensureFS();
  $('fs-modal').classList.add('open');
  fsLoad('');
}
function fsClose(){
  if(!$('fs-modal')){ return; }
  $('fs-modal').classList.remove('open');
  fsState.cb=null;
}
function ensureFS(){
  if($('fs-modal')){ return; }
  var m=h('div',{id:'fs-modal'},
    '<div class="fs-dialog">'+
    '<div class="fs-head"><div class="fs-title">Choose a folder to mount</div><button type="button" class="iconbtn fs-x" title="close">✕</button></div>'+
    '<div class="fs-pathbar"><button type="button" id="fs-up" title="up one level">↑</button><input id="fs-cur" spellcheck="false" autocomplete="off"><button type="button" id="fs-go">Go</button></div>'+
    '<div class="fs-crumb" id="fs-crumb"></div>'+
    '<div class="fs-list" id="fs-list"></div>'+
    '<div class="fs-foot"><span class="fs-sel" id="fs-selfile"></span><button type="button" id="fs-home" title="back to your home folder">Home</button><button type="button" id="fs-cancel">Cancel</button><button type="button" class="primary" id="fs-ok">Use this folder</button></div>'+
    '</div>');
  document.body.appendChild(m);
  m.querySelector('.fs-x').addEventListener('click',fsClose);
  $('fs-cancel').addEventListener('click',fsClose);
  $('fs-go').addEventListener('click',function(){ fsLoad($('fs-cur').value.trim()); });
  $('fs-cur').addEventListener('keydown',function(ev){ if(ev.key==='Enter'){ fsLoad(this.value.trim()); } });
  $('fs-up').addEventListener('click',function(){ if(fsState.parent){ fsLoad(fsState.parent); } });
  $('fs-home').addEventListener('click',function(){ if(fsState.home){ fsLoad(fsState.home); } });
  $('fs-ok').addEventListener('click',function(){
    var p=(fsState.sel&&fsState.sel.length)?fsState.sel:fsState.path;
    if(!p){ return; }
    var cb=fsState.cb;
    fsClose();
    if(cb){ cb(p); }
  });
  m.addEventListener('mousedown',function(ev){ if(ev.target===m){ fsClose(); } });
  document.addEventListener('keydown',function(ev){ if(ev.key==='Escape'){ fsClose(); } });
}
function fsLoad(p){
  var list=$('fs-list');
  list.innerHTML='<div class="fs-empty">loading…</div>';
  fetch('/api/fs?path='+encodeURIComponent(p||'')).then(function(r){ return r.json(); }).then(function(j){
    if(j.error){ list.innerHTML='<div class="fs-empty">'+esc(j.error)+'</div>'; return; }
    fsRender(j);
  }).catch(function(){ list.innerHTML='<div class="fs-empty">browse failed</div>'; });
}
function fsRender(j){
  var list=$('fs-list');
  fsState.path=j.path; fsState.home=j.home||''; fsState.parent=j.parent||''; fsState.sel='';
  $('fs-cur').value=j.path;
  $('fs-up').disabled=!j.parent;
  $('fs-home').disabled=!fsState.home;
  $('fs-ok').textContent='Use this folder';
  $('fs-selfile').textContent='';
  fsCrumbs(j.path);
  list.innerHTML='';
  if(!j.entries.length){ list.innerHTML='<div class="fs-empty">empty folder</div>'; return; }
  j.entries.forEach(function(e){
    var p2=j.path.replace(/\/+$/,'')+'/'+e.name;
    var row=h('div',{class:'fs-row '+(e.dir?'dir':'file')},
      (e.dir?icoDir:icoFile)+'<span class="grow">'+esc(e.name)+'</span>'+
      (e.dir?'<button type="button" class="chev" title="open">›</button>':''));
    row.addEventListener('click',function(){
      Array.prototype.forEach.call(list.querySelectorAll('.fs-row.sel'),function(x){ x.classList.remove('sel'); });
      row.classList.add('sel');
      fsState.sel=p2;
      $('fs-selfile').textContent=p2;
      $('fs-ok').textContent=e.dir?'Use folder':'Use file';
    });
    if(e.dir){
      var open=function(){ fsLoad(p2); };
      row.addEventListener('dblclick',open);
      row.querySelector('.chev').addEventListener('click',function(ev){ ev.stopPropagation(); open(); });
    }
    list.appendChild(row);
  });
}
function fsCrumbs(path){
  var host=$('fs-crumb');
  host.innerHTML='';
  var acc='';
  path.split('/').filter(Boolean).forEach(function(p){
    acc+='/'+p;
    var b=h('button',{class:'crumb'},esc(p));
    b.addEventListener('click',function(){ fsLoad(acc); });
    host.appendChild(b);
    host.appendChild(h('span',{class:'csep'},'/'));
  });
  var chips=host.querySelectorAll('.crumb');
  if(chips.length){ chips[chips.length-1].classList.add('cur'); }
}

function drillToggle(name){
  var row=document.querySelector('.drillrow[data-hbd="'+name+'"]');
  if(!row){ return; }
  if(!row.hidden){ row.hidden=true; return; }
  row.hidden=false;
  var host=$('drill-'+name);
  host.innerHTML='<div class="subwrap"><div class="hint">loading detail…</div></div>';
  fetch('/api/usage?name='+encodeURIComponent(name)).then(function(r){return r.json();}).then(function(s){
    var html='<div class="subwrap"><div class="kv">today $'+(s.today_usd||0).toFixed(4)+' · total $'+(s.total_usd||0).toFixed(4)+
      ' · tokens in/out '+(s.tokens_in||0)+'/'+(s.tokens_out||0)+'</div>';
    if(s.models && s.models.length){
      html+='<table class="tbl"><tr><th>model</th><th>in</th><th>out</th><th>est</th><th>$</th><th>calls</th></tr>';
      s.models.forEach(function(m){
        html+='<tr><td>'+esc(m.model)+'</td><td>'+m.in+'</td><td>'+m.out+'</td><td>'+(m.est_calls||'')+'</td><td>'+(m.cost||0).toFixed(4)+'</td><td>'+m.calls+'</td></tr>';
      });
      html+='</table>';
    }
    if(s.recent && s.recent.length){
      html+='<table class="tbl recent"><tr><th>when</th><th>model</th><th>in/out</th><th>est</th><th>ms</th><th>code</th></tr>';
      s.recent.slice(0,10).forEach(function(e){
        html+='<tr><td>'+esc((e.ts||'').replace('T',' ').substr(0,19))+'</td><td>'+esc(e.model)+'</td><td>'+e.in+'/'+e.out+'</td><td>'+(e.est?'yes':'')+'</td><td>'+e.dur_ms+'</td><td>'+e.status+'</td></tr>';
      });
      html+='</table>';
    }
    html+='<div id="px-'+esc(name)+'" class="kv">proxy: loading…</div></div>';
    host.innerHTML=html;
    fetch('/api/sandboxes/'+kindFor(name)+'/'+encodeURIComponent(name)).then(function(r){return r.json();}).then(function(dj){
      var p=dj.proxy||{};
      var px=$('px-'+name);
      if(px){ px.innerHTML='proxy: <code>'+esc(p.base_url||'-')+'</code> - key '+(p.key_masked ? esc(p.key_masked)+' ('+esc(p.provider||'?')+')' : 'not synced yet'); }
  }).catch(function(){ console.warn('dashboard: request failed silently in drillToggle'); });
  }).catch(function(){ host.innerHTML='<div class="subwrap hint">detail unavailable</div>'; });
}

function renderBurnTab(sb){
  var host=$('burn-hbd');
  if(!sb){ host.innerHTML='<div class="hint">no sandbox selected</div>'; return; }
  // the detail burn tab polls on the shared lease too: while THIS sandbox's
  // detail view is open the cards/table stay live; ticks skip while the user
  // works an input. The reload button (tab-burn pane) forces a fresh read.
  if(burnTimer){ clearInterval(burnTimer); burnTimer=null; }
  var finish=function(){
    burnTimer=setInterval(function(){
      var ae=document.activeElement;
      if(state.view!=='sandbox'||!state.sel||state.sel.indexOf('/'+sb.name)<0){ clearInterval(burnTimer); burnTimer=null; return; }
      if(ae&&(ae.tagName==='INPUT'||ae.tagName==='TEXTAREA'||ae.tagName==='SELECT')) return;
      renderBurnTab(sb);
    },5000);
  };
  host.innerHTML='<div class="hint">loading…</div>';
  fetch('/api/usage?name='+encodeURIComponent(sb.name)).then(function(r){return r.json();}).then(function(s){
    var cards='<div class="burnbar">'+
      '<div class="bstat"><div class="v">$'+(s.today_usd||0).toFixed(4)+'</div><div class="k">burn today</div></div>'+
      '<div class="bstat"><div class="v">$'+(s.total_usd||0).toFixed(4)+'</div><div class="k">burn total</div></div>'+
      '<div class="bstat"><div class="v">'+fmtTok(s.today_tokens_in)+' / '+fmtTok(s.today_tokens_out)+'</div><div class="k">tok today in / out</div></div>'+
      '<div class="bstat"><div class="v">'+fmtTok(s.tokens_in)+' / '+fmtTok(s.tokens_out)+'</div><div class="k">tok all-time in / out</div></div>'+
      '<div class="bstat"><div class="v">'+(s.calls||0)+'</div><div class="k">calls</div></div>'+
      '</div>';
    var html=cards;
    if(s.models && s.models.length){
      html+='<table class="tbl"><thead><tr><th>model</th><th>in</th><th>out</th><th>est</th><th>$</th><th>calls</th></tr></thead><tbody>';
      s.models.forEach(function(m){
        html+='<tr><td>'+esc(m.model)+'</td><td>'+fmtTok(m.in)+'</td><td>'+fmtTok(m.out)+'</td><td>'+(m.est_calls||'')+'</td><td>'+((m.cost||0).toFixed(4))+'</td><td>'+m.calls+'</td></tr>';
      });
      html+='</tbody></table>';
    } else {
      html+='<div class="hint">No calls through the proxy yet. Sync this sandbox (button on the burn overview) and use opencode inside it.</div>';
    }
    if(s.recent && s.recent.length){
      html+='<div class="k" style="font-size:.66rem;color:var(--faint);text-transform:uppercase;letter-spacing:.06em;margin:14px 0 4px">recent calls</div>';
      html+='<table class="tbl recent"><thead><tr><th>time</th><th>model</th><th>in</th><th>out</th><th>$</th><th>status</th></tr></thead><tbody>';
      s.recent.forEach(function(e){
        html+='<tr><td>'+esc((e.ts||'').replace('T',' ').slice(0,19))+'</td><td>'+esc(e.model)+'</td><td>'+fmtTok(e.in)+'</td><td>'+fmtTok(e.out)+'</td><td>'+((e.cost||0).toFixed(4))+'</td><td>'+(e.status||'')+((e.est)?'<span class="chipmuted">est</span>':'')+'</td></tr>';
      });
      html+='</tbody></table><div class="hint">est = token estimate (bytes/4 - used when the provider reports no usage)</div>';
    }
    host.innerHTML=html;
    finish();
  }).catch(function(){ burnRetry(host, function(){ renderBurnTab(sb); }); });
}

/* ---------- dns tab ---------- */
/* What a container looked up (recorded by the DNS fence while tracking is
   on) + allowlist toggles to open forgotten domains after the fact. Polls
   every 5 s while visible; the table body is patched in place so the
   filter input keeps focus. */
var dnsTimer=null, dnsBusy=false, dnsFilter='', dnsOnlyBlocked=false, dnsSB=null, dnsLast=null;
function dnsURL(sb){ return '/api/sandboxes/container/'+encodeURIComponent(sb.name)+'/dns'; }
function renderDnsTab(sb){
  var host=$('dns-body');
  if(dnsTimer){ clearInterval(dnsTimer); dnsTimer=null; }
  if(!sb){ host.innerHTML='<div class="hint">no sandbox selected</div>'; return; }
  if(sb.kind!=='container'){ host.innerHTML='<div class="hint">DNS tracking covers container sandboxes (egress fences are container-only).</div>'; return; }
  if(!dnsSB || dnsSB.name!==sb.name){ host.innerHTML='<div class="hint">loading…</div>'; host.removeAttribute('data-sig'); }
  dnsSB=sb;
  var load=function(){
    fetch(dnsURL(sb)).then(function(r){return r.json();}).then(function(j){
      if(dnsSB!==sb || dnsBusy) return;
      if(j.error){ host.innerHTML='<div class="hint">'+esc(j.error)+'</div>'; host.removeAttribute('data-sig'); return; }
      paintDns(sb,j);
    }).catch(function(){ if(dnsSB===sb && !host.getAttribute('data-sig')){ burnRetry(host, function(){ renderDnsTab(sb); }); } });
  };
  load();
  dnsTimer=setInterval(function(){
    if(state.view!=='sandbox'||activeTab!=='dns'||state.sel!==keyOf(sb)){ clearInterval(dnsTimer); dnsTimer=null; return; }
    if(!dnsBusy) load();
  },5000);
}
function dnsPost(sb, body){
  dnsBusy=true;
  Array.prototype.forEach.call(document.querySelectorAll('#dns-body input[type=checkbox], #dns-body button'),function(el){ el.disabled=true; });
  return fetch(dnsURL(sb),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
    .then(function(r){return r.json();})
    .then(function(j){
      dnsBusy=false;
      $('dns-body').removeAttribute('data-sig'); // controls re-enable via a full repaint
      if(j.error){ toast('dns: '+j.error,'err'); renderDnsTab(sb); return; }
      paintDns(sb,j);
    })
    .catch(function(){ dnsBusy=false; toast('dns: request failed','err'); $('dns-body').removeAttribute('data-sig'); renderDnsTab(sb); });
}
function dnsWhen(t){
  if(!t) return '<span class="dim">-</span>';
  var d=new Date(t); if(isNaN(d)) return esc(t);
  var sec=Math.max(0,(Date.now()-d.getTime())/1000);
  if(sec<60) return Math.round(sec)+'s ago';
  if(sec<3600) return Math.round(sec/60)+'m ago';
  if(sec<86400) return Math.round(sec/3600)+'h ago';
  return esc(d.toISOString().replace('T',' ').slice(0,16));
}
function paintDns(sb,j){
  var host=$('dns-body');
  dnsLast=j;
  var recorded=!!j.mode, mode=j.mode||'allow', track=!!j.track;
  var fenceable=(mode==='allow'||mode==='allowlist');
  var sig=mode+'|'+track+'|'+recorded;
  if(host.getAttribute('data-sig')!==sig || !$('dns-tbl')){
    var expl;
    if(mode==='allowlist'){
      expl='<b>allowlist</b> fence: only listed domains resolve, everything else is refused and dropped. Flip a toggle to allow a domain - it applies live, no restart. Entries match their subdomains too.';
      if(!track){ expl+=' Tracking is off, so refused lookups are not recorded - turn it on to see what the sandbox is missing.'; }
    } else if(mode==='allow'){
      expl=track
        ? '<b>allow</b> + tracking: outbound is open, every lookup passes and lands here. The toggles collect an allowlist; <i>Lock down</i> switches this sandbox to the allowlist preset with exactly those domains.'
        : '<b>allow</b>: outbound is open and lookups are not recorded. Turn tracking on to see which domains this sandbox (and its agent) reaches.';
      if(!recorded){ expl+=' <span class="dim">(no preset recorded for this sandbox - treated as the docker default)</span>'; }
    } else {
      expl='This sandbox runs the <b>'+esc(mode)+'</b> preset - DNS tracking needs <span class="mono">allow</span> or <span class="mono">allowlist</span> (<span class="mono">hotbedd egress '+esc(sb.name)+' allowlist --track</span>).';
    }
    var presetBtn='';
    if(mode==='allow' && track){ presetBtn='<button class="tool" id="dns-lock" title="switch to the allowlist preset using the toggled domains">&#128274; Lock down to allowlist</button>'; }
    if(mode==='allowlist'){ presetBtn='<button class="tool" id="dns-open" title="switch back to the allow preset (tracking stays as is)">Open egress (allow)</button>'; }
    host.innerHTML=
      '<div class="hint">'+expl+'</div>'+
      '<div class="burnbar">'+
        '<div class="bstat"><div class="v" id="dns-n-dom">0</div><div class="k">domains seen</div></div>'+
        '<div class="bstat"><div class="v" id="dns-n-q">0</div><div class="k">lookups</div></div>'+
        '<div class="bstat" id="dns-blk-card"><div class="v" id="dns-n-blk">0</div><div class="k">'+(mode==='allowlist'?'blocked domains':'not on the list')+'</div></div>'+
        '<div class="bstat"><div class="v" id="dns-n-list">0</div><div class="k">allowlist entries</div></div>'+
      '</div>'+
      '<div class="dnstools">'+
        '<label title="record every DNS lookup of this sandbox"><span class="ios-toggle"><input type="checkbox" id="dns-track"'+(track?' checked':'')+(fenceable?'':' disabled')+'><span class="track"></span><span class="knob"></span></span>record lookups</label>'+
        '<label><span class="ios-toggle"><input type="checkbox" id="dns-onlyblk"'+(dnsOnlyBlocked?' checked':'')+'><span class="track"></span><span class="knob"></span></span>only '+(mode==='allowlist'?'blocked':'unlisted')+'</label>'+
        '<input id="dns-filter" type="search" placeholder="filter domains" autocomplete="off" spellcheck="false">'+
        '<span class="grow"></span>'+presetBtn+
        '<button class="tool" id="dns-clear" title="forget the recorded lookups (the allowlist stays)">&#128465; clear record</button>'+
      '</div>'+
      '<div id="dns-tbl"></div>';
    host.setAttribute('data-sig',sig);
    $('dns-filter').value=dnsFilter;
    $('dns-filter').addEventListener('input',function(){ dnsFilter=this.value.trim().toLowerCase(); if(dnsLast) dnsTable(sb,dnsLast); });
    $('dns-onlyblk').addEventListener('change',function(){ dnsOnlyBlocked=this.checked; if(dnsLast) dnsTable(sb,dnsLast); });
    $('dns-track').addEventListener('change',function(){ dnsPost(sb,{action:'track',on:this.checked}); });
    $('dns-clear').addEventListener('click',function(){
      askConfirm({title:'Clear DNS record of '+sb.name+'?', body:'Drops every recorded lookup. The allowlist and the fence stay as they are.', confirmLabel:'Clear', danger:true},
        function(ok){ if(ok) dnsPost(sb,{action:'clear'}); });
    });
    if($('dns-lock')) $('dns-lock').addEventListener('click',function(){
      var n=(dnsLast&&dnsLast.allowlist||[]).length;
      askConfirm({title:'Lock '+sb.name+' down to its allowlist?',
        body:'From now on only the <b>'+n+'</b> listed domain(s) (and the hotbed proxy) stay reachable; every other lookup is refused and dropped. Tracking stays on, so forgotten domains show up here to allow.',
        confirmLabel:'Lock down'}, function(ok){ if(ok) dnsPost(sb,{action:'preset',preset:'allowlist'}); });
    });
    if($('dns-open')) $('dns-open').addEventListener('click',function(){
      askConfirm({title:'Open egress for '+sb.name+'?', body:'Removes the fence: the sandbox may reach any address again. The allowlist is kept for a later lock-down.', confirmLabel:'Open egress', danger:true},
        function(ok){ if(ok) dnsPost(sb,{action:'preset',preset:'allow'}); });
    });
    $('dns-tbl').addEventListener('change',function(ev){
      var t=ev.target; if(!t || !t.getAttribute('data-i') || !dnsLast) return;
      var r=(dnsLast.domains||[])[parseInt(t.getAttribute('data-i'),10)]; if(!r) return;
      dnsPost(sb,{action:t.checked?'allow':'deny',domain:r.domain});
    });
  }
  dnsTable(sb,j);
}
function dnsTable(sb,j){
  var mode=j.mode||'allow', fenceable=(mode==='allow'||mode==='allowlist');
  var rows=j.domains||[], q=0, blk=0;
  rows.forEach(function(r){ q+=r.count||0; if(!r.allowed && r.count) blk++; });
  $('dns-n-dom').textContent=rows.filter(function(r){return r.count;}).length;
  $('dns-n-q').textContent=q;
  $('dns-n-blk').textContent=blk;
  $('dns-blk-card').classList.toggle('warn', mode==='allowlist' && blk>0);
  $('dns-n-list').textContent=(j.allowlist||[]).length;
  var html='<table class="tbl"><thead><tr><th>allow</th><th>domain</th><th>status</th><th>lookups</th><th>refused</th><th>last seen</th><th>first seen</th></tr></thead><tbody>';
  var shown=0;
  rows.forEach(function(r,i){
    if(dnsFilter && r.domain.indexOf(dnsFilter)<0) return;
    if(dnsOnlyBlocked && r.allowed) return;
    shown++;
    var via=(r.allowed && !r.listed)? r.covered_by : '';
    var dis=(!fenceable || dnsBusy || via)?' disabled':'';
    var tip=via? ' title="allowed via the entry '+esc(via)+'"' : '';
    var st;
    if(r.allowed){ st='<span class="dnsst ok">allowed</span>'; }
    else if(mode==='allowlist'){ st='<span class="dnsst bad">blocked</span>'; }
    else { st='<span class="dnsst">open</span>'; }
    html+='<tr><td class="tg"><span class="ios-toggle"'+tip+'><input type="checkbox" data-i="'+i+'"'+(r.allowed?' checked':'')+dis+'><span class="track"></span><span class="knob"></span></span></td>'+
      '<td>'+esc(r.domain)+(via?'<span class="via">via '+esc(via)+'</span>':'')+'</td><td>'+st+'</td>'+
      '<td>'+(r.count||0)+'</td><td>'+(r.blocked||0)+'</td><td>'+dnsWhen(r.last)+'</td><td>'+dnsWhen(r.first)+'</td></tr>';
  });
  html+='</tbody></table>';
  if(!rows.length){
    html='<div class="hint">'+(j.track? 'No lookups recorded yet - they appear here within a few seconds of the sandbox resolving a name.' : 'Nothing recorded - tracking is off.')+'</div>';
  } else if(!shown){
    html+='<div class="hint">no rows match the filter</div>';
  }
  $('dns-tbl').innerHTML=html;
}

/* ---------- help & CLI reference ---------- */
function cliTable(rows){
  return '<table class="tbl"><tbody>'+rows.map(function(r){
    return '<tr><td><code>'+esc(r[0])+'</code></td><td style="color:var(--dim);font-size:.76rem">'+r[1]+'</td></tr>';
  }).join('')+'</tbody></table>';
}
function hsec(t){ return '<div class="hsec">'+t+'</div>'; }
function renderHelp(){
  var sql=$('glob-body');
  sql.innerHTML=
    hsec('The idea')+
    '<div style="font-size:.82rem;color:var(--dim);line-height:1.55;max-width:70ch">'+
    '<p><b>hbd</b> makes a disposable Docker container per project dir - your project is bind-mounted at <span class="mono">/workspace</span>, the only host path the agent can touch. <b>hotbedvm</b> is the VM escape hatch (libvirt, or docker-in-docker "hotbedvmd"): snapshots, ssh, full disk. <b>hotbedd</b> is this daemon and dashboard: OpenCode config, the proxy (burn ledger), wallets, egress fences, kits.</p>'+
    '<p>Provider API keys stay on the host in hotbedd only - sandboxes get per-sandbox minted proxy keys, the UI shows them masked everywhere.</p>'+
    '<p><b>Egress fences</b>: <span class="mono">none</span> keeps only loopback+established (everything else drops, DNS included). <span class="mono">proxy-only</span> additionally keeps private ranges and DNS open so the host-side proxy works while public addresses drop. <span class="mono">allowlist</span> routes all DNS through the fence and only lets the sandbox reach domains from its configured list. With <b>DNS tracking</b> (allow / allowlist) every lookup shows up in the sandbox&rsquo;s DNS tab, where a toggle per domain allows it after the fact.</p>'+
    '</div>'+
    hsec('LLM disclosure')+
    '<div style="font-size:.82rem;color:var(--dim);line-height:1.55;max-width:70ch">'+
    '<p>This project is developed with LLM coding agents (via OpenCode) under human direction: much of the code, tests and docs was authored or reworked by an assistant, then reviewed and gate-verified before landing. The README\'s <b>LLM disclosure</b> section has the full statement.</p>'+
    '</div>'+
    hsec('Quick start')+
    '<table class="tbl"><tbody>'+
    '<tr><td style="text-align:right;color:var(--faint);font-size:.72rem;width:2rem">1.</td><td style="color:var(--dim);font-size:.76rem">Start the daemon: <code>hotbedd serve --root &lt;repo&gt;</code> - dashboard at http://127.0.0.1:7770</td></tr>'+
    '<tr><td style="text-align:right;color:var(--faint)">2.</td><td style="color:var(--dim);font-size:.76rem">Import your providers once: <code>hotbedd config init</code> (copies from host OpenCode config; keys stay in hotbedd)</td></tr>'+
    '<tr><td style="text-align:right;color:var(--faint)">3.</td><td style="color:var(--dim);font-size:.76rem">Day-to-day: <code>hbd shell</code> / <code>hbd opencode</code> in a project dir, or <b>+ New sandbox</b> in the sidebar (VMs via hotbedvm)</td></tr>'+
    '<tr><td style="text-align:right;color:var(--faint)">4.</td><td style="color:var(--dim);font-size:.76rem">Route a sandbox through the proxy: <code>hotbedd sync NAME</code> - burn rows appear on the Burn tab as it calls</td></tr>'+
    '<tr><td style="text-align:right;color:var(--faint)">5.</td><td style="color:var(--dim);font-size:.76rem">Sanity check any time: <code>hotbedd doctor</code></td></tr>'+
    '</tbody></table>'+
    hsec('hbd - project containers')+
    cliTable([
      ['hbd|hbd shell','shell inside this project\'s sandbox'],
      ['hbd opencode [args]','run the OpenCode TUI inside the sandbox'],
      ['hbd exec [--] cmd / hbd root [--] cmd','run one command as user / as root'],
      ['hbd status|stop|rm','manage THIS project\'s sandbox; rm deletes it fully'],
      ['hbd ls / hbd prune','list all hbd sandboxes / remove all'],
      ['hbd --stack NAME --fresh --net none','options at spawn: stack, ephemeral, network fence'],
      ['hbd caches ls|nuke / hbd build','shared package caches / (re)build images'],
      ['hbd doctor','diagnose the hbd environment']
    ])+
    hsec('hotbedvm - KVM VMs')+
    cliTable([
      ['hotbedvm fetch-image','download the Debian cloud image (once)'],
      ['hotbedvm create NAME --share DIR','create + boot a VM (also --cpu/--mem/--disk)'],
      ['hotbedvm ssh NAME|shell NAME','ssh in (dev@); <code>hotbedvm opencode NAME</code> for the TUI'],
      ['hotbedvm snapshot NAME save|revert|list','snapshot - roll back everything the agent did'],
      ['hotbedvm ip NAME / hotbedvm start|stop NAME','power control / address'],
      ['hotbedvm console NAME','serial console (boot debugging)'],
      ['hotbedvm destroy NAME --yes','delete the VM and its disks irrevocably'],
      ['HOTBEDVM_BACKEND=libvirt|docker','backend choice; docker = hotbedvmd containers running qemu']
    ])+
    hsec('hotbedd - daemon, config, burn')+
    cliTable([
      ['hotbedd serve --port N','start the resident daemon (dashboard)'],
      ['hotbedd ps|stats|events','unified sandbox table; stats adds live cpu/mem'],
      ['hotbedd logs NAME [-f]','container logs / VM serial'],
      ['hotbedd attach NAME [--oc]','attach a persistent tmux session (with the OpenCode TUI)'],
      ['hotbedd start|stop|rm NAME','engine control; rm also revokes keys, wallets, sync target'],
      ['hotbedd config init|print|set','providers import / masked print / set proxy.port etc.'],
      ['hotbedd usage [NAME]','the burn ledger from the CLI'],
      ['hotbedd key mint|ls|revoke','per-sandbox proxy keys'],
      ['hotbedd budget [NAME] [USD] [--tokens N]','wallets (Burn tab "set" button does the same)'],
      ['hotbedd price ls|set','$/Mtok pricing used for the $ columns'],
      ['hotbedd sync NAME|all','push proxy-backed OpenCode config into sandbox(es)'],
      ['hotbedd mount ls|add|rm','host bind mounts of a container (add/rm recreates it)'],
      ['hotbedd recipe print|up|validate / hotbedd egress','declarative hbd.yaml sandboxes / outbound fence'],
      ['hotbedd audit [N]','control-plane audit trail']
    ])+
    hsec('How burn is counted')+
    '<div style="font-size:.8rem;color:var(--dim);line-height:1.55;max-width:70ch">'+
    '<ul style="margin:.2rem 0 .2rem 1.1rem;padding:0">'+
    '<li>Every request a synced sandbox routes through the daemon proxy is journaled per sandbox in <span class="mono">~/.local/state/hotbed/usage.jsonl</span> (tokens in/out, model, cost if priced).</li>'+
    '<li><b>$ columns</b> come from the price table in <span class="mono">config.json</span> ($/Mtok, longest-prefix match per model). Models priced at 0 show $0.0000 - the token counts still walk.</li>'+
    '<li><b>est rows</b>: the provider did not report usage - tokens are estimated from response size (bytes/4) and marked est.</li>'+
    '<li><b>Wallets</b> (budget USD / budget tokens per sandbox) trip a 429 when exhausted (per UTC day) - the sandbox gets a clear message instead of burning through the ceiling; raise with <code>hotbedd budget NAME [USD] [--tokens N]</code>. Mirrored in the Burn table via the "set" button.</li>'+
    '<li>Stale rows = the sandbox was deleted (its history stays until you purge or pin it). Live rows update every 5 s while the tab is open.</li>'+
    '</ul></div>'+
    hsec('Troubleshooting')+
    '<div style="font-size:.8rem;color:var(--dim);line-height:1.55;max-width:70ch">'+
    '<ul style="margin:.2rem 0 .2rem 1.1rem;padding:0">'+
    '<li><b>Sandbox gets ECONNREFUSED on the proxy</b>: that is the daemon-side relay container (<span class="mono">sbx-relay-*</span>). The daemon re-establishes its relay automatically at boot; check <code>docker ps</code> for it and run <code>hotbedd doctor</code>.</li>'+
    '<li><b>Changed config/providers but sandbox sees nothing</b>: sandboxes only see proxy settings after a sync - run <code>hotbedd sync NAME</code> (or the sync button on the Burn tab).</li>'+
    '<li><b>Attaching again</b>: <code>hotbedd attach NAME</code> re-uses the sandbox\'s tmux session; after a <code>hotbedd sync</code> re-attach once so the session environment picks up the synced env.</li>'+
    '<li><b>Boot debugging</b> on a VM: <code>hotbedvm console NAME</code> (serial), <code>hotbedvm ip NAME</code>, and <code>hotbedd logs NAME -f</code>.</li>'+
    '<li><b>Burn tab blank</b> while <code>curl 127.0.0.1:7770/api/usage</code> works: reload once - the page is served no-store, and the retry button re-reads the ledger.</li>'+
    '</ul></div>';
}

function renderConfigGlobal(){
  var body=$('glob-body');
  body.innerHTML='<div class="hint">Providers hold the upstream endpoints and keys. They stay in hotbedd; sandboxes receive only their minted proxy key. Values shown as <code>&lt;masked&gt;</code> are preserved on save. After editing providers run <code>hotbedd sync</code> (or the sync button on the burn view).</div>'+
    '<textarea id="cfg-editor" spellcheck="false"></textarea>'+
    '<div class="warnline" id="cfg-warn"></div>';
  fetch('/api/config').then(function(r){return r.json();}).then(function(cfg){
    var ed=$('cfg-editor'); if(ed){ ed.value=JSON.stringify(cfg,null,2); }
  }).catch(function(){ console.warn('dashboard: request failed silently in renderConfigGlobal'); });
  var save=addTool($('glob-actions'),'Save',function(){
    var raw=$('cfg-editor').value, j;
    try{ j=JSON.parse(raw); }catch(e){ $('cfg-warn').textContent='not valid JSON: '+e.message; return; }
    void j;
    fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:raw})
      .then(function(r){return r.json();})
      .then(function(res){ if(res.error){ $('cfg-warn').textContent=res.error; } else { $('cfg-warn').textContent=''; toast('config saved - proxy picks it up live','ok'); } });
  });
  void save;
  var imp=addTool($('glob-actions'),'Import host opencode.json',function(){
    fetch('/api/config/import',{method:'POST'})
      .then(function(r){return r.json();})
      .then(function(res){
        if(res.error){ $('cfg-warn').textContent=res.error; return; }
        toast('imported '+res.imported+' provider(s)','ok');
        fetch('/api/config').then(function(r){return r.json();}).then(function(cfg){
          var ed=$('cfg-editor'); if(ed){ ed.value=JSON.stringify(cfg,null,2); }
        });
      });
  });
  void imp;
}

/* ---------- details ---------- */
/* after a create succeeds, hop straight to the new machine's page instead of
   parking on the form. The collector may lag one refresh tick (~5s), so poll
   the REST snapshot briefly; past the budget, land honestly on the list. */
function jumpToCreated(kind, name, warnTxt){
  var tries=0;
  function finish(){
    state.view='sandbox'; $('global').hidden=true; showEmpty();
  }
  function attempt(){
    fetch('/api/sandboxes').then(function(r){return r.json();}).then(function(d){
      var sb=(d.sandboxes||[]).filter(function(x){return x.name===name&&x.kind===kind;})[0];
      if(sb){ if(warnTxt){ toast(warnTxt,'ok'); } select(sb); return; }
      if(++tries<20){ setTimeout(attempt,500); return; }
      finish(); toast('created '+name+' - still surfacing in the sidebar','ok');
    }).catch(function(){
      if(++tries<20){ setTimeout(attempt,500); return; }
      finish(); toast('created '+name,'ok');
    });
  }
  attempt();
}

function select(sb){
  clearTimers();
  state.sel=keyOf(sb);
  state.view='sandbox';                 // leaving any global view
  closeLogs();
  renderList();
  $('empty').hidden=true; $('pane').hidden=false; $('global').hidden=true;  var vmKind=sb.kind==='vm';
  $('d-name').textContent=sb.name;
  $('d-sub').innerHTML = (vmKind? esc('VM · ') : '')+ esc(sb.kind)+(vmKind && sb.backend? ' · '+esc(sb.backend):'')+
     (sb.project? ' · '+esc(short(sb.project)):'') + (sb.stack? ' · '+esc(sb.stack):'');
  updateButtons(sb);
  renderOverview(sb);
  renderConn(sb);
  // keep the burned tab live across sandbox switches too - switching
  // machines with the burn pane open must swap the data, not just the name
  if(activeTab==='burn'){ renderBurnTab(sb); }
  if(activeTab==='logs'){ openLogs(sb); }
  // DNS tracking rides the container egress fence - VMs have no DNS tab
  $('tabbtn-dns').hidden=(sb.kind!=='container');
  if(activeTab==='dns'){
    if(sb.kind==='container'){ renderDnsTab(sb); }
    else { document.querySelector('.tab[data-tab="overview"]').click(); }
  }
  saveView();
}
function renderOverview(sb){
  var stat=function(v){return {v:v};};
  var cards=[
    {t:'state', v:pillFor(sb)+' '+esc(sb.status||''), cls:'stat'},
    {t:'image', v:sb.image?esc(sb.image):'<span class="dim">-</span>'},
    {t:'cpu', v: sb.cpuPercent!=null? sb.cpuPercent.toFixed(2)+'%':'-', cls:'stat'},
    {t:'memory', v: sb.memUsage||'-', cls:'stat'},
  ];
  var extra=[];
  cardrowRender($('ov-stats'), cards);
  if(sb.ports && sb.ports.length) extra.push({t:'ports',v:sb.ports.map(esc).join(', ')});
  extra.push({t:'created',v:esc(sb.uptime||sb.createdAt||'-')});
  if(sb.netIO) extra.push({t:'net i/o',v:esc(sb.netIO)});
  if(sb.blockIO) extra.push({t:'disk i/o',v:esc(sb.blockIO)});
  cardrowRender($('ov-cards'), extra);
  s.FetchDetail(sb);
}
function cardrowRender(host, cards){
  host.innerHTML='';
  cards.forEach(function(c){
    var el=h('div',{class:'card'+(c.cls?' '+c.cls:'')},'<h4>'+esc(c.t)+'</h4><div class="v">'+(c.v==null?'-':c.v)+'</div>');
    host.appendChild(el);
  });
}
/* detail fetch merges mounts/env into overview */
var s={FetchDetail:function(sb){
  fetch('/api/sandboxes/'+sb.kind+'/'+sb.name).then(function(r){return r.json();}).then(function(j){
    if(!j || !j.detail) { $('ov-cards').appendChild(h('div',{class:'card'},'<h4>detail</h4><div class="v small">container detail needs docker inspect (daemon has it)</div>')); if(j && j.conn) fillDetailConn(j.conn); return; }
    var d=j.detail; extras=[];
    if(j.snapshots && j.snapshots.length){
      extras.push({t:'snapshot timeline ('+j.snapshots.length+')',v:'<ul class="sntl">'+j.snapshots.map(function(s){
        return '<li>'+esc(s.tag)+(s.info?' <span class="small">'+esc(s.info)+'</span>':'')+'</li>'; }).join('')+'</ul>'+
        '<div class="hint small">save/restore via CLI: hotbedvm snapshot '+esc(sb.name)+' save (stops the VM briefly) · hotbedvm snapshot '+esc(sb.name)+' list</div>'});
    } else if(sb.kind==='vm'){
      extras.push({t:'snapshot timeline',v:'<div class="hint small">no snapshots yet - hotbedvm snapshot '+esc(sb.name)+' save takes one (offline: the VM stops for the save, then reboots)</div>'});
    }
    if(d.mounts && d.mounts.length){
      extras.push({t:'mounts',v:'<ul>'+d.mounts.map(function(m){
        return '<li>'+esc((m.rw?'rw ':'ro ')+m.target+' ← '+short(m.source))+'</li>'; }).join('')+'</ul>'});
    }
    if(d.workdir) extras.push({t:'workdir',v:esc(d.workdir)});
    if(d.env && d.env.length){
      var ENV=d.env.slice(0,26).map(function(e){return '<li>'+esc(e)+'</li>';}).join('');
      extras.push({t:'env ('+d.env.length+', masked secrets)',v:'<ul>'+ENV+'</ul>'});
    }
    if(d.createdAt) extras.push({t:'started',v:esc(d.createdAt)});
    cardrowRender($('ov-cards'), extra.concat(extras));
  }).catch(function(){ console.warn('dashboard: request failed silently in cardrowRender'); });
}};
var extra=[], extras=[];

/* ---------- connection tab ---------- */
function renderConn(sb){
  var host=$('conn-rows'); host.innerHTML='';

  fetch('/api/sandboxes/'+sb.kind+'/'+sb.name+'/connect?mode=shell')
   .then(function(r){return r.json();})
   .then(function(ci){
      var rows=[];
      if(ci.shellCmd)    rows.push({l:sb.kind==='vm'?'ssh (primary)':'shell (primary)', v:ci.shellCmd, copy:ci.shellCmd});
      if(ci.backendCmd && sb.kind==='container') rows.push({l:'opencode one-shot', v:ci.backendCmd, copy:ci.backendCmd});
      if(ci.wtCommand)   rows.push({l:'Windows Terminal', v:ci.wtCommand, copy:ci.wtCommand});
      if(ci.tmuxAttach)  rows.push({l:'re-attach tmux session', v:ci.tmuxAttach, copy:ci.tmuxAttach});
      if(ci.tmuxSession) rows.push({l:'tmux session', v:ci.tmuxSession});
      rows.forEach(function(r){
        var row=h('div',{class:'row'},
          '<div class="lbl">'+esc(r.l)+'</div><code>'+esc(r.v)+'</code>'+
          (r.copy? '<button data-copy="'+esc(r.copy)+'">copy</button>':''));
        host.appendChild(row);
        if(r.copy){ row.querySelector('button').addEventListener('click', function(){ copy(r.copy); }); }
      });
   });
}
function fillDetailConn(ci){ /* reserved */ }

/* ---------- logs tab ---------- */
function openLogs(sb){
  closeLogs();
  var box=$('logbox'); box.textContent='';
  $('log-state').textContent='streaming…';
  var es=new EventSource('/api/sandboxes/'+sb.kind+'/'+sb.name+'/logs');
  state.esLogs=es;
  es.addEventListener('log', function(ev){
    var line=ev.data;
    try{ var p=JSON.parse(line); if(typeof p==='string') line=p; }catch(_E){ /* send raw on parse failure */ }
    appendLog(line);
  });
  es.onerror=function(){ $('log-state').textContent='stream closed'; es.close(); state.esLogs=null; };
}
function closeLogs(){ if(state.esLogs){ state.esLogs.close(); state.esLogs=null; } $('logbox').textContent=''; }
function appendLog(line){
  var box=$('logbox');
  if(typeof line==='string' && line.length>8000){ line=line.slice(0,8000)+'  …(+line truncated)'; }
  box.appendChild(document.createTextNode(line+'\n'));
  while(box.childNodes.length>4000){ box.removeChild(box.firstChild); }
  if($('log-follow').checked){ box.scrollTop=box.scrollHeight; }
}

/* ---------- actions ---------- */
function act(kind,name,what){
  var pkey=kind+':'+name;
  // optimistic UX: the daemon round-trip can take a while (docker's stop
  // grace period, VM ACPI shutdowns) - reflect the intent immediately
  state.pending[pkey]=what;
  var pend=toast((what==='stop'?'stopping ':'starting ')+name+' - waiting for the daemon (stop waits out the grace period; VMs shut down gracefully)…','pend');
  renderList();
  updateButtons(currentSB());
  return fetch('/api/actions/'+what,{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({kind:kind,name:name})}).then(function(r){return r.json();}).then(function(j){
      if(pend) pend.remove();
      delete state.pending[pkey];
      if(j.error){ toast((what==='stop'?'stop':'start')+': '+j.error,'err'); }
      else { toast((what==='start'?'started ':'stopped ')+name+' - daemon confirmed','ok'); }
      updateButtons(currentSB());
      renderList();
    }).catch(function(){
      if(pend) pend.remove();
      delete state.pending[pkey];
      toast(what+' request failed (network) - check daemon connection','err');
      updateButtons(currentSB());
      renderList();
    });
}
function delSB(sb){
  if(!sb) return;
  var isVM = sb.kind === 'vm';
  var doDel=function(){
    fetch('/api/actions/delete',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({kind:sb.kind,name:sb.name,confirm:isVM})}).then(function(r){return r.json();}).then(function(j){
        if(j.error) toast('delete: '+j.error,'err'); else toast('deleted '+sb.name,'ok');
      });
  };
  askConfirm({
    title:'Delete sandbox?',
    body:'Delete sandbox <span class="mono">'+esc(sb.name)+'</span>?',
    confirmLabel:'Delete '+sb.kind,
    danger:true,
    attrs:['Its container/VM, minted proxy keys, wallets and sync target are removed.']
  },function(ok){
    if(!ok){ return; }
    if(!isVM){ doDel(); return; }
    askConfirm({
      title:'Fully wipe the VM?',
      body:'Delete the VM <span class="mono">'+esc(sb.name)+'</span> for good?',
      confirmLabel:'Wipe VM forever',
      danger:true,
      attrs:['This wipes the VM disk <b>and snapshots</b> - irrevocable.']
    },function(ok2){ if(ok2){ doDel(); } });
  });
}
function attachGo(sb, mode, spawn){
  if((sb.state||'').toLowerCase()==='booting'){
    toast(sb.name+' is still booting (kit self-registration) - the terminal opens right after the guest finishes booting','err');
    return;
  }
  return fetch('/api/sandboxes/'+sb.kind+'/'+sb.name+'/attach',{
    method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({mode:mode,spawn:!!spawn})})
   .then(function(r){return r.json();})
   .then(function(j){
     if(j.error){ toast('attach: '+j.error,'err'); return; }
     var c=j.conn||{};
     if(c.spawnError){ toast('terminal: '+c.spawnError+' - connection copied instead','err'); copy(c.tmuxAttach||c.wtCommand||c.shellCmd||''); return; }
     if(spawn){ toast('Opening Windows Terminal… attach with: '+(c.tmuxAttach||''),'ok'); copy(c.tmuxAttach||''); }
     else { toast('tmux session "'+c.tmuxSession+'" ready - attach: '+(c.tmuxAttach||''),'ok'); copy(c.tmuxAttach||''); }
   });
}

/* ---------- health ---------- */
function refreshHealth(){
  fetch('/api/health').then(function(r){return r.json();}).then(function(j){
    state.health=j; $('ver').textContent='v'+j.version;
    setBadge('h-docker', j.docker); setBadge('h-libvirt', j.libvirt); setBadge('h-tmux', j.tmux);
  }).catch(function(){ console.warn('dashboard: request failed silently in refreshHealth'); });
}
function setBadge(id, ok){ var el=$(id); el.textContent=id.replace('h-','')+(ok?' up':' down'); el.className='hbadge '+(ok?'up':'down'); }

/* ---------- SSE inventory ---------- */
function openEvents(){
  if(state.es) state.es.close();
  var es=new EventSource('/api/events');
  state.es=es;
  es.addEventListener('snapshot', function(ev){
    // contract: ev.data is a bare array. Older daemons shipped null or a
    // wrapped {sandboxes:…} object - normalize all three to an array so
    // renderList can never see anything but an array.
    try{
      var d=JSON.parse(ev.data);
      state.list=Array.isArray(d) ? d : (d && Array.isArray(d.sandboxes) ? d.sandboxes : []);
    }catch(e){ return; }
    state.firstData=true;
    renderList();
    if(state.sel){
      var cur=state.list.find(function(x){return keyOf(x)===state.sel;});
      if(cur){
        updateButtons(cur);
        if(activeTab==='overview') renderOverview(cur);
        if(activeTab==='conn') renderConn(cur);
        if(activeTab==='burn') renderBurnTab(cur);
      }
    }
  });
  es.onerror=function(){ /* EventSource auto-reconnects */
    if(!state.firstData){
      Array.prototype.forEach.call(document.querySelectorAll('.fspin-msg'),function(m){
        m.textContent='connection hiccup - retrying…';
      });
    }
  };
}
function updateButtons(sb){
  if(!sb){ $('btn-start').disabled=true; $('btn-stop').disabled=true; return; }
  var pend=state.pending[sb.kind+':'+sb.name];
  var lock=!!pend; // while a start/stop is in flight, lock both buttons
  $('btn-start').disabled= lock || sb.state==='running';
  $('btn-stop').disabled = lock || sb.state!=='running';
}

/* ---------- tabs ---------- */
var activeTab='overview';
function initTabs(){
  Array.prototype.forEach.call(document.querySelectorAll('.tab'),function(t){
    t.addEventListener('click',function(){
      activeTab=t.getAttribute('data-tab');
      saveView();
      Array.prototype.forEach.call(document.querySelectorAll('.tab'),function(x){x.classList.toggle('active',x===t);});
      ['overview','logs','conn','burn','dns'].forEach(function(id){ $('tab-'+id).hidden=(id!==activeTab); });
      if(activeTab==='logs' && state.sel){ var sb=currentSB(); if(sb) openLogs(sb); }
      if(activeTab==='burn'){ var sb2=currentSB(); renderBurnTab(sb2); }
      if(activeTab==='dns'){ renderDnsTab(currentSB()); }
      if(activeTab!=='logs'){ closeLogs(); }
    });
  });
  $('log-clear').addEventListener('click', function(){ $('logbox').textContent=''; });
}
function currentSB(){
  return state.list.find(function(x){return keyOf(x)===state.sel;}) || null;
}

/* ---------- wiring ---------- */
/* boot restore: a page reload should land on the view the user left
   (global view, or a sandbox detail even). Unknown sel -> overview. */
function restoreBoot(){
  var st=null;
  try{ st=JSON.parse(localStorage.getItem('hotbed-view')||'null'); }catch(e){}
  if(st && st.view==='sandbox' && typeof st.sel==='string' && st.sel.indexOf('/')>0){
    var i=st.sel.indexOf('/');
    var sb=state.list.find(function(x){return keyOf(x)===st.sel;});
    if(sb){ select(sb); }
    else { select({kind:st.sel.slice(0,i), name:st.sel.slice(i+1)}); } // header now; snapshot fills the rest
    if(st.tab && document.querySelector('.tab[data-tab="'+st.tab+'"]')){
      document.querySelector('.tab[data-tab="'+st.tab+'"]').click();
    }
    return;
  }
  if(st && st.view && typeof st.view==='string' && st.view!=='sandbox'){
    showGlobal(st.view);
    return;
  }
  showGlobal('overview'); // default: host overview
}
function init(){
  initTabs(); refreshHealth(); openEvents();
  renderList(); // paint the loading spinner right away - the first snapshot may take seconds
  setInterval(refreshHealth, 12000);
  $('theme-toggle').addEventListener('click', function(){
    var next=document.documentElement.getAttribute('data-theme')==='light'?'dark':'light';
    document.documentElement.setAttribute('data-theme',next);
    try{ localStorage.setItem('hotbed-theme',next); }catch(e){}
  });
  // patience hint while the very first inventory scan is still running
  setTimeout(function(){
    if(!state.firstData){
      Array.prototype.forEach.call(document.querySelectorAll('.fspin-msg'),function(m){
        m.textContent='still looking - initial docker/virsh scans can take a moment…';
      });
    }
  }, 20000);

  $('btn-attach').addEventListener('click', function(){ var sb=currentSB(); if(sb) attachGo(sb,'shell',true); });
  $('btn-oc').addEventListener('click', function(){ var sb=currentSB(); if(sb) attachGo(sb,'opencode',false); });
  $('btn-copy').addEventListener('click', function(){
    var sb=currentSB(); if(!sb) return;
    fetch('/api/sandboxes/'+sb.kind+'/'+sb.name+'/connect?mode=shell')
      .then(function(r){return r.json();})
      .then(function(ci){ copy(ci.shellCmd || ci.wtCommand || ci.tmuxAttach || ''); }); });
  $('btn-start').addEventListener('click', function(){ var sb=currentSB(); if(sb) act(sb.kind,sb.name,'start'); });
  $('btn-stop').addEventListener('click', function(){ var sb=currentSB(); if(sb) act(sb.kind,sb.name,'stop'); });
  $('btn-del').addEventListener('click', function(){ var sb=currentSB(); if(sb) delSB(sb); });
  $('lnk-help').addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('help'); });
  // the brand does the old bottom-entry job: click it for the Overview
  $('brand').addEventListener('click', function(){ showGlobal('overview'); });
  $('burn-reload').addEventListener('click', function(){ var sb=currentSB(); if(sb) renderBurnTab(sb); });
  $('lnk-burn').addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('burn'); });
  var lc=$('lnk-create');
  if(lc){ lc.addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('create'); }); }
  Array.prototype.forEach.call(document.querySelectorAll('[data-create]'), function(el){
    el.addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('create'); });
  });
  $('lnk-config').addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('config'); });
  restoreBoot(); // start page: whatever the user left open (fallback: overview)
}
document.addEventListener('DOMContentLoaded', init);
`
