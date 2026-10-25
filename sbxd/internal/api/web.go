package api

// Embedded dashboard: one page, vanilla JS + SSE, no build step.

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>SBX Desktop — OpenCode Sandboxes</title>
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<header id="topbar">
  <div id="brand"><span class="logo">SBX</span><span class="brandname">Desktop</span><span id="ver"></span></div>
  <div id="health">
    <span class="hbadge" id="h-docker">docker</span>
    <span class="hbadge" id="h-libvirt">libvirt</span>
    <span class="hbadge" id="h-tmux">tmux</span>
  </div>
</header>

<div id="shell">
  <aside id="sidebar">
    <a href="#" id="lnk-create" class="cta">＋ New sandbox</a>
    <div class="side-h">Containers <span class="cnt" id="cnt-c"></span></div>
    <div class="items" id="items-c"></div>
    <div class="side-h">VMs · libvirt <span class="cnt" id="cnt-vl"></span></div>
    <div class="items" id="items-vl"></div>
    <div class="side-h">VMs · docker (sbxvmd) <span class="cnt" id="cnt-vd"></span></div>
    <div class="items" id="items-vd"></div>
    <div id="sidebar-foot">
      <a href="#" id="lnk-burn">Burn — tokens &amp; budget</a>
      <a href="#" id="lnk-config">OpenCode config</a>
      <a href="#" id="lnk-help">Help &amp; CLI</a>
    </div>
  </aside>

  <main id="main">
    <div id="empty">
      <div class="emptycard">
        <h3>No sandbox selected</h3>
        <p>Pick one from the sidebar, or spin up a fresh kit-faithful sandbox.</p>
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
        <div class="hint">Per-sandbox token ledger: fed by the sbxd proxy (upstream usage or chars/4 estimate — marked).</div>
        <div id="burn-sbx"></div>
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

const styleCSS = `/* SBX Desktop design system — hand-rolled, no framework */
:root{
  --bg:#0d1015; --bg2:#131722; --bg3:#1a2230; --bg4:#222c3d;
  --fg:#e7ecf3; --dim:#8d99ab; --faint:#5d6b7e;
  --acc:#5b8cff; --acc2:#7aa5ff; --acc-soft:rgba(91,140,255,.16);
  --grn:#4cc38a; --red:#e5484d; --yel:#e7a43b;
  --line:#232b3a; --line2:#2e3a4e;
  --mono:ui-monospace,"SFMono-Regular",Menlo,Consolas,monospace;
  --sans:system-ui,-apple-system,"Segoe UI",Roboto,"Inter",sans-serif;
  --r1:6px; --r2:10px; --r3:14px;
  --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s5:24px; --s6:32px;
  --shadow:0 10px 30px rgba(2,6,14,.5);
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
.item{padding:.5rem .6rem;border-radius:var(--r2);cursor:pointer;display:flex;align-items:center;gap:var(--s2);border:1px solid transparent}
.item:hover{background:var(--bg3)}
.item.sel{background:var(--acc-soft);border-color:rgba(91,140,255,.35);box-shadow:inset 2px 0 0 var(--acc)}
.item .nm{flex:1;min-width:0}
.item .nm b{display:block;font-weight:600;font-size:.84rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.item .nm small{color:var(--faint);display:block;font-size:.7rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.item .meters{font-size:.66rem;color:var(--faint);text-align:right;line-height:1.3;flex:none}
.pill{font-size:.62rem;border-radius:999px;padding:.1rem .42rem;font-weight:700;color:var(--dim);background:var(--bg4);border:1px solid var(--line2);text-transform:uppercase;letter-spacing:.03em;align-self:center}
.pill.running{background:rgba(76,195,138,.12);color:var(--grn);border-color:rgba(76,195,138,.4)}
.pill.created{background:rgba(91,140,255,.12);color:var(--acc2);border-color:rgba(91,140,255,.4)}
.pill.paused,.pill.restarting{background:rgba(231,164,59,.12);color:var(--yel);border-color:rgba(231,164,59,.4)}
.pill.exited,.pill.dead,.pill.stopped{background:rgba(229,72,77,.1);color:#f08a8d;border-color:rgba(229,72,77,.35)}
.pill.shutoff{background:rgba(231,164,59,.08);color:#cbb27a;border-color:rgba(231,164,59,.25)}
.pill.booting{background:rgba(231,164,59,.14);color:var(--yel);border-color:rgba(231,164,59,.5);animation:sbxpulse 1.5s ease-in-out infinite}
.pill.stopping,.pill.starting{background:rgba(231,164,59,.14);color:var(--yel);border-color:rgba(231,164,59,.5);animation:sbxpulse 1.5s ease-in-out infinite}
@keyframes sbxpulse{from{opacity:1}50%{opacity:.55}to{opacity:1}}
/* ---------- first-load spinner (until the first inventory snapshot) ---------- */
.fspin{display:flex;align-items:center;gap:8px;color:var(--faint);font-size:.74rem;padding:12px 2px;letter-spacing:.02em}
.spin{width:13px;height:13px;border:2px solid var(--line2);border-top-color:var(--acc);border-radius:50%;animation:sbxspin .8s linear infinite;flex:none}
@keyframes sbxspin{to{transform:rotate(360deg)}}
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
button.danger:hover{border-color:var(--red);color:var(--red);background:rgba(229,72,77,.08)}
button:disabled{opacity:.4;cursor:default;pointer-events:none}
button.tool{background:var(--bg3);border:1px solid var(--line2);border-radius:var(--r1);padding:.28rem .6rem;font-size:.74rem}
.iconbtn{background:none;border:none;color:var(--faint);font-size:.8rem;padding:.3rem .5rem;border-radius:var(--r1);line-height:1}
.iconbtn:hover{color:var(--red);background:rgba(229,72,77,.1);border:none}
input,select,textarea{font-family:var(--sans)}
input[type=text],input:not([type]),select,textarea{background:#0b0f16;border:1px solid var(--line2);color:var(--fg);border-radius:var(--r1);padding:.4rem .6rem;font-size:.8rem;height:32px}
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
#logbox{background:#0a0d12;border:1px solid var(--line);border-radius:var(--r2);padding:var(--s3) var(--s4);height:min(52vh,640px);overflow:auto;font:11.5px/1.55 var(--mono);white-space:pre-wrap;word-break:break-word;margin:0}

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
input.money{width:78px;background:#0b0f16;border:1px solid var(--line2);color:var(--fg);padding:.3rem .45rem;border-radius:var(--r1);font:.75rem var(--mono)}
input.money.big{width:100%}
textarea#cfg-editor{width:100%;min-height:54vh;background:#0a0d13;color:var(--fg);border:1px solid var(--line2);border-radius:var(--r2);padding:var(--s4);font:.78rem/1.55 var(--mono);white-space:pre;resize:vertical}
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
.chipmuted{font-size:.6rem;color:var(--yel);border:1px solid rgba(231,164,59,.5);border-radius:999px;padding:.05rem .45rem;margin-left:.4rem;text-transform:uppercase;letter-spacing:.05em;vertical-align:middle}
.starpin{background:none;border:none;color:var(--faint);cursor:pointer;font-size:1rem;padding:0 .15rem;line-height:1}
.starpin:hover{color:var(--yel)}
.starpin.on{color:#f5c542}

/* ---------- toasts ---------- */
#toasts{position:fixed;right:var(--s4);bottom:var(--s4);display:flex;flex-direction:column;gap:var(--s2);z-index:50}
.toast{background:var(--bg3);border:1px solid var(--line2);border-left:4px solid var(--acc);border-radius:var(--r2);padding:.7rem 1rem;font-size:.84rem;font-weight:500;line-height:1.4;max-width:540px;min-width:280px;box-shadow:0 12px 30px rgba(2,6,16,.55)}
.toast.ok{border-left-color:var(--grn)}
.toast.err{border-left-color:var(--red)}
.toast.pend{border-left-color:var(--yel);animation:sbxpend 1.2s ease-in-out infinite}
@keyframes sbxpend{0%,100%{box-shadow:0 12px 30px rgba(2,6,16,.55)}50%{box-shadow:0 12px 30px rgba(2,6,16,.55),0 0 0 3px rgba(231,164,59,.28)}}

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
label.check .box,label.ro .box{width:15px;height:15px;border-radius:4px;border:1px solid var(--line2);background:#0b0f16;display:inline-block;position:relative;flex:none;transition:background .12s,border-color .12s}
label.check input:checked~.box,label.ro input:checked~.box{background:var(--acc);border-color:var(--acc)}
label.check input:checked~.box::after,label.ro input:checked~.box::after{content:"";position:absolute;left:4.6px;top:1.6px;width:4px;height:8px;border:solid #fff;border-width:0 2px 2px 0;transform:rotate(45deg)}
label.check:hover .box,label.ro:hover .box{border-color:var(--acc)}
.rows-t{font-size:.74rem;color:var(--dim);margin:var(--s3) 0 var(--s2)}
.rows{display:flex;flex-direction:column;gap:var(--s2)}
.drow{display:flex;align-items:center;gap:var(--s2);background:#0b0f16;border:1px solid var(--line);border-radius:var(--r2);padding:.45rem .6rem}
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
/* SBX Desktop dashboard — vanilla JS, SSE-driven. No build step. */
var state = { list: [], sel: null, es: null, esLogs: null, health: null, view: 'sandbox', firstData: false, pending: {} };

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
  if(navigator.clipboard && navigator.clipboard.writeText){
    navigator.clipboard.writeText(text).then(function(){ toast('Copied to clipboard','ok'); },
      function(){ toast('Clipboard blocked — see Connection tab','err'); });
  }else{ toast('Clipboard unavailable','err'); }
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
  var sub=sb.kind==='container' ? (sb.stack||'container')+' · '+short(sb.project||sb.image||'')
        : (sb.backend==='docker'?'sbxvmd':sb.backend)+(sb.ports&&sb.ports.length?' · '+sb.ports[0]:'');
  el.innerHTML='<div class="nm"><b>'+esc(sb.name)+'</b><small>'+esc(sub)+'</small></div>'+
    '<div class="meters">'+meters(sb)+'</div>'+pillFor(sb);
  el.addEventListener('click', function(){ select(sb); });
  return el;
}
function short(s){ return (s||'').replace(/^\/home\//,'~/'); }
function keyOf(sb){ return sb.kind+'/'+sb.name; }

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

/* ---------- global views (burn / config) ---------- */
function showGlobal(kind){
  state.view=kind; state.sel=null; closeLogs();
  renderList();
  $('pane').hidden=true; $('empty').hidden=true; $('global').hidden=false;
  $('glob-title').textContent = kind==='burn' ? 'Burn — token & dollar ledger' : kind==='create' ? 'Create sandbox' : 'Config — global OpenCode settings';
  $('glob-sub').textContent = kind==='burn' ? 'fed by the sbxd proxy — upstream keys never reach sandboxes' : kind==='create' ? 'kit-faithful container via the daemon — mounts and ports bake in at creation' : 'providers, default model, proxy port, prices — saved in ~/.config/sbxd/config.json';
  $('glob-actions').innerHTML='';
  $('glob-body').innerHTML='';
  if(kind==='burn'){ renderBurnGlobal(); } else if(kind==='create'){ renderCreator(); } else { renderConfigGlobal(); }
}
function kindFor(name){
  var sb=state.list.filter(function(x){return x.name===name;})[0];
  return sb ? sb.kind : 'vm';
}
function addTool(bar,label,fn){
  var b=h('button',{class:'tool'},esc(label));
  b.addEventListener('click',fn); bar.appendChild(b); return b;
}

function renderBurnGlobal(){
  var body=$('glob-body');
  body.innerHTML='<div class="hint">loading…</div>';
  fetch('/api/usage').then(function(r){return r.json();}).then(function(j){
    var rows=j.rows||[];
    if(!rows.length){ body.innerHTML='<div class="hint">No usage yet. Flow: sbxd config init → sbxd sync NAME → sandbox calls go through the proxy → rows appear here.</div>'; return; }
    var todaySum=0,allSum=0,live=0,staleUnseen=0,staleTotal=0;
    rows.forEach(function(r){
      todaySum+=r.today_usd||0; allSum+=r.total_usd||0;
      if(r.exists){ live++; } else { staleTotal++; if(!r.pinned) staleUnseen++; }
    });
    var bar='<div class="burnbar">'+
      '<div class="bstat"><div class="v">$'+todaySum.toFixed(4)+'</div><div class="k">burn today</div></div>'+
      '<div class="bstat"><div class="v">$'+allSum.toFixed(4)+'</div><div class="k">burn all-time</div></div>'+
      '<div class="bstat"><div class="v">'+live+'</div><div class="k">live sandboxes</div></div>'+
      '<div class="bstat'+(staleUnseen>0?' warn':'')+'"><div class="v">'+staleUnseen+'</div><div class="k">stale history rows</div></div>'+
      '</div>'+
      '<div class="burntools">'+
        '<label><input type="checkbox" id="burn-show-stale"'+(burnShowStale?' checked':'')+'> show stale rows</label>'+
        '<button class="tool" id="burn-purge">🗑 purge stale history'+(staleUnseen?' ('+staleUnseen+')':'')+'</button>'+
        '<span class="hint">stale = sandbox deleted · ★ pinned histories stay visible and survive the bulk purge</span>'+
      '</div>';
    var tbl='<table class="tbl" id="burntbl"><thead><tr><th></th><th>sandbox</th><th>state</th><th>today $</th><th>total $</th><th>calls</th><th>tok today</th><th>budget $</th><th>budget tok</th><th></th></tr></thead><tbody>';
    rows.forEach(function(r){
      var stale=!r.exists;
      var hidden=stale&&!r.pinned&&!burnShowStale;
      var dim=stale?' class="rowdim"':'';
      var state=stale?'<span class="chipmuted">stale</span>':'<span class="chipmuted" style="color:var(--grn);border-color:rgba(76,195,138,.5)">live</span>';
      var pin='<button class="starpin'+(r.pinned?' on':'')+' tpin" data-sbx="'+esc(r.sandbox)+'" data-p="'+(r.pinned?1:0)+'" title="'+(r.pinned?'unpin':'pin burn history')+'">'+(r.pinned?'★':'☆')+'</button>';
      var bud;
      if(stale){
        bud='<td>—</td><td>—</td>';
      }else{
        bud='<td><input class="money" data-sbx="'+esc(r.sandbox)+'" value="'+(r.budget_usd>0? r.budget_usd : '')+'"></td>'+
            '<td><input class="money" data-tok="'+esc(r.sandbox)+'" value="'+(r.budget_tokens>0? r.budget_tokens : '')+'"></td>';
      }
      var acts;
      if(stale){
        acts='<button class="tool trowdel" data-sbx="'+esc(r.sandbox)+'">delete records</button>';
      }else{
        acts='<button class="tool tbud" data-sbx="'+esc(r.sandbox)+'">set</button>'+
             '<button class="tool tdrill" data-sbx="'+esc(r.sandbox)+'">detail</button>'+
             '<button class="tool tsync" data-sbx="'+esc(r.sandbox)+'">sync</button>';
      }
      tbl+='<tr'+dim+(hidden?' data-hidden="1"':'')+' data-sbx="'+esc(r.sandbox)+'"><td>'+pin+'</td><td>'+esc(r.sandbox)+'&nbsp;&nbsp;'+state+'</td>'+
        '<td>$'+(r.today_usd||0).toFixed(4)+'</td><td>$'+(r.total_usd||0).toFixed(4)+'</td><td>'+(r.calls||0)+'</td><td>'+fmtTok(r.tokens_today)+'</td>'+
        bud+'<td class="actions">'+acts+'</td></tr>'+
        (stale?'':'<tr class="drillrow" data-sbx="'+esc(r.sandbox)+'" hidden><td colspan="10" id="drill-'+esc(r.sandbox)+'"></td></tr>');
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
          if(!confirm('Delete the burn history of '+staleUnseen+' stale (unpinned) sandbox row(s)? Pinned ★ histories are kept.')) return;
          if(!confirm('Second confirm: this permanently removes their journal rows from usage.jsonl. Continue?')) return;
          fetch('/api/usage/purge',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({stale_only:true})})
            .then(function(r){return r.json();}).then(function(res){
              if(res.error){ toast('purge: '+res.error,'err'); }
              else { toast('purged '+(res.purged||[]).length+' stale record set(s), '+(res.rows||0)+' rows dropped','ok'); renderBurnGlobal(); }
            });
        });
      }
    }
    Array.prototype.forEach.call(body.querySelectorAll('.tpin'),function(b){
      b.addEventListener('click',function(){
        var name=b.getAttribute('data-sbx'); var to=b.getAttribute('data-p')==='1'?false:true;
        fetch('/api/usage/pin',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:name,pinned:to})})
          .then(function(r){return r.json();}).then(function(res){
            if(res.error){ toast('pin: '+res.error,'err'); }
            else { toast((res.pinned?'pinned ':'unpinned ')+name,'ok'); renderBurnGlobal(); }
          });
      });
    });
    Array.prototype.forEach.call(body.querySelectorAll('.trowdel'),function(b){
      b.addEventListener('click',function(){
        var name=b.getAttribute('data-sbx');
        if(!confirm("Delete the burn records of "+name+"? Its sandbox no longer exists; the history lives only in usage.jsonl.")) return;
        fetch('/api/usage/purge',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sandboxes:[name]})})
          .then(function(r){return r.json();}).then(function(res){
            if(res.error){ toast('purge: '+res.error,'err'); }
            else { toast('deleted records for '+name+' ('+(res.rows||0)+' rows)','ok'); renderBurnGlobal(); }
          });
      });
    });
    Array.prototype.forEach.call(body.querySelectorAll('.tbud'),function(b){
      b.addEventListener('click',function(){
        var name=b.getAttribute('data-sbx');
        var inp=body.querySelector('input.money[data-sbx="'+name+'"]');
        var tok=body.querySelector('input.money[data-tok="'+name+'"]');
        var usd=(inp.value==='')?null:(parseFloat(inp.value)||0);
        var toks=(tok.value==='')?null:(parseInt(tok.value,10)||0);
        fetch('/api/usage/budget',{method:'POST',headers:{'Content-Type':'application/json'},
          body:JSON.stringify({name:name,usd:usd,tokens:toks})}).then(function(r){return r.json();})
          .then(function(res){ if(res.error) toast('budget: '+res.error,'err'); else toast('wallets set for '+name,'ok'); });
      });
    });
    Array.prototype.forEach.call(body.querySelectorAll('.tdrill'),function(b){
      b.addEventListener('click',function(){ drillToggle(b.getAttribute('data-sbx')); });
    });
    Array.prototype.forEach.call(body.querySelectorAll('.tsync'),function(b){
      b.addEventListener('click',function(){
        var name=b.getAttribute('data-sbx'); var kind=kindFor(name);
        fetch('/api/sandboxes/'+kind+'/'+encodeURIComponent(name)+'/sync',{
          method:'POST',headers:{'Content-Type':'application/json'},body:'{}'})
          .then(function(r){return r.json();})
          .then(function(res){ if(res.error){ toast('sync: '+res.error,'err'); } else { toast(name+' synced to proxy ('+(res.target.key_masked||'')+')','ok'); } });
      });
    });
  }).catch(function(){ body.innerHTML='<div class="hint">burn data unavailable</div>'; });
}

/* ---------- create wizard (POST /api/create) ---------- */
var icoDir='<svg class="fi" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M1.5 3.2C1.5 2.5 2 2 2.7 2h3.2c.4 0 .8.2 1 .5l.9 1h5.5c.7 0 1.2.5 1.2 1.2v7.1c0 .7-.5 1.2-1.2 1.2H2.7c-.7 0-1.2-.5-1.2-1.2V3.2Z"/></svg>';
var icoFile='<svg class="fi" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path fill="currentColor" d="M3.4 1.1h5.2l3.9 3.9v9.4c0 .5-.4.9-.9.9H3.4c-.5 0-.9-.4-.9-.9V2c0-.5.4-.9.9-.9Z"/></svg>';

function renderCreator(){
  var body=$('glob-body');
  var stackOpts=['go','node','python','rust','base'].map(function(s){return '<option'+(s==='go'?' selected':'')+'>'+s+'</option>';}).join('');
  body.innerHTML=
    '<div class="hint">Creates a sandbox through the daemon. <b>Containers</b> are kit-faithful (dev.sbx labels, opencode auth mounts, per-stack cache volume). <b>VMs</b> boot a full Debian guest with docker + opencode preinstalled (libvirt &quot;sbxvm&quot; or docker-backend &quot;sbxvmd&quot;) — creation blocks until the guest is up (~1–2 min; the cloud master image must be fetched once via <code>sbxvm fetch-image</code>).</div>'+
    '<div class="cw">'+
    '<div class="cw-card">'+
    '<div class="cw-sec"><div class="cw-sec-t">Sandbox kind</div>'+
      '<div class="cw-kind">'+
        '<label class="check"><input type="radio" name="cwkind" value="container" checked><span class="box"></span> container (sbx)</label>'+
        '<label class="check"><input type="radio" name="cwkind" value="vm-libvirt"><span class="box"></span> VM · libvirt (sbxvm)</label>'+
        '<label class="check"><input type="radio" name="cwkind" value="vm-docker"><span class="box"></span> VM · docker (sbxvmd)</label>'+
      '</div>'+
      '<div class="fm" id="cw-kindmsg">containers share the host kernel — seconds to spawn, mounts/ports bake in at creation.</div>'+
    '</div>'+
    '<div class="cw-sec"><div class="cw-sec-t">Basics</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-name">Name</label><input id="cw-name" placeholder="myproj" autocomplete="off" spellcheck="false" maxlength="40"><div class="fm" id="cw-namemsg"></div></div>'+
      '<div class="field" id="cw-stack-field"><label for="cw-stack">Stack image</label><select id="cw-stack">'+stackOpts+'</select><div class="fm">needs the kit image — sbx build &lt;stack&gt; builds it once</div></div>'+
    '</div></div>'+
    '<div id="cw-cont-fields">'+
    '<div class="cw-sec"><div class="cw-sec-t">Network</div><div class="cw-grid two">'+
      '<div class="field"><label for="cw-egress">Egress</label><select id="cw-egress"><option>allow</option><option>proxy-only</option><option>none</option></select><div class="fm">outbound fence; re-apply after recreates</div></div>'+
    '</div>'+
    '<div class="rows-t">Published ports — host → container</div>'+
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
      '<div class="field"><label for="cw-vmshare">Share directory (host)</label><input id="cw-vmshare" placeholder="optional — e.g. ~/projects/myapp" spellcheck="false"><div class="fm">mounted at /workspace in the guest — empty = VM-local workspace</div></div>'+
    '</div>'+
    '<div class="fm">the guest gets its root user + dev user with your uid inside; opencode connects through the sbx proxy after a sync (button on the Burn overview).</div>'+
    '</div>'+
    '<div class="cw-sec"><div class="cw-sec-t">OpenCode</div><div class="cw-grid two">'+
      '<div class="field"><label>Sync to sbx proxy</label><label class="check"><input type="checkbox" id="cw-sync" checked><span class="box"></span>mint sandbox key &amp; write transport</label><div class="fm">provider keys never enter the sandbox</div></div>'+
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
  nameDupCheck();
  Array.prototype.forEach.call(document.querySelectorAll('input[name="cwkind"]'),function(rd){
    rd.addEventListener('change',updateCreatorKind);
  });
  fetch('/api/health').then(function(r){return r.json();}).then(function(h){
    if(h && h.libvirt===false){
      var rb=document.querySelector('input[name="cwkind"][value="vm-docker"]');
      if(rb && !document.querySelector('input[name="cwkind"]:checked[value^="vm"]')){ rb.checked=true; updateCreatorKind(); }
    }
  }).catch(function(){});
  $('cw-go').addEventListener('click',function(){
    var name=($('cw-name').value||'').trim().toLowerCase();
    var msg=$('cw-msg');
    msg.textContent='';
    if(!name){ msg.textContent='name required'; return; }
    var kindSel=(document.querySelector('input[name="cwkind"]:checked')||{}).value||'container';
    if(kindSel!=='container'){
      msg.textContent='provisioning VM — this blocks until the guest is up (minutes on first boot)…';
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
          if(res.error){ msg.innerHTML='<span class="err">'+esc('create: '+res.error)+'</span>'; toast('create: '+res.error,'err'); return; }
          var w=res.warn?' (warnings: '+res.warn+')':'';
          msg.textContent='created VM '+res.vm+' ('+res.backend+' backend, '+res.cpu+' cpu / '+res.mem+' MB / '+res.disk+' GB)'+w+' — appears in the sidebar in ~5s · '+(res.attach||'sbxd attach');
          toast('created VM '+res.vm,'ok');
        })
        .catch(function(){ msg.textContent='create failed (network) — the VM may still be provisioning; check sbxd ps'; });
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
    if($('cw-usd').value!==''){ payload.budget_usd=parseFloat($('cw-usd').value)||0; }
    if($('cw-tok').value!==''){ payload.budget_tokens=parseInt($('cw-tok').value,10)||0; }
    msg.textContent='creating…';
    fetch('/api/create',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
      .then(function(r){return r.json();})
      .then(function(res){
        if(res.error){ msg.innerHTML='<span class="err">'+esc('create: '+res.error)+'</span>'; toast('create: '+res.error,'err'); return; }
        var w=res.warn?' (warnings: '+res.warn+')':'';
        msg.textContent='created '+res.container+w+' — appears in the sidebar in ~5s · attach: '+(res.attach||'sbx attach');
        toast('created '+res.container,'ok');
      })
      .catch(function(){ msg.textContent='create failed (network)'; });
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
    km.textContent = k==='container' ? 'containers share the host kernel — seconds to spawn, mounts/ports bake in at creation.' :
      k==='vm-libvirt' ? 'libvirt VM on the host (sbxvm-<name>) — needs qemu:///system; strongest isolation.' :
      'docker-backend VM (sbxvmd-<name>) — QEMU in a container; works where libvirt is unavailable (e.g. Docker Desktop).';
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
    var dup=n && state.list.some(function(sb){ return sb.name===n || (k==='container' && sb.name==='sbx-'+n); });
    m.textContent=dup ? '"'+n+'" already exists — pick another name' :
      (k==='container' ? 'container becomes sbx-<name> — a–z, 0–9, -' : 'VM keeps this name (sbxvm-<name> / sbxvmd-<name>) — letters, digits, . _ -');
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
var fsState={cb:null,path:'',sel:'',parent:''};
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
    '<div class="fs-foot"><span class="fs-sel" id="fs-selfile"></span><button type="button" id="fs-cancel">Cancel</button><button type="button" class="primary" id="fs-ok">Use this folder</button></div>'+
    '</div>');
  document.body.appendChild(m);
  m.querySelector('.fs-x').addEventListener('click',fsClose);
  $('fs-cancel').addEventListener('click',fsClose);
  $('fs-go').addEventListener('click',function(){ fsLoad($('fs-cur').value.trim()); });
  $('fs-cur').addEventListener('keydown',function(ev){ if(ev.key==='Enter'){ fsLoad(this.value.trim()); } });
  $('fs-up').addEventListener('click',function(){ if(fsState.parent){ fsLoad(fsState.parent); } });
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
  fsState.path=j.path; fsState.parent=j.parent||''; fsState.sel='';
  $('fs-cur').value=j.path;
  $('fs-up').disabled=!j.parent;
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
  var row=document.querySelector('.drillrow[data-sbx="'+name+'"]');
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
      if(px){ px.innerHTML='proxy: <code>'+esc(p.base_url||'—')+'</code> — key '+(p.key_masked ? esc(p.key_masked)+' ('+esc(p.provider||'?')+')' : 'not synced yet'); }
    }).catch(function(){});
  }).catch(function(){ host.innerHTML='<div class="subwrap hint">detail unavailable</div>'; });
}

function renderBurnTab(sb){
  var host=$('burn-sbx');
  if(!sb){ host.innerHTML='<div class="hint">no sandbox selected</div>'; return; }
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
      html+='</tbody></table><div class="hint">est = usage chars/4-estimated (upstream did not report)</div>';
    }
    host.innerHTML=html;
  }).catch(function(){ host.innerHTML='<div class="hint">burn data unavailable</div>'; });
}

function renderConfigGlobal(){
  var body=$('glob-body');
  body.innerHTML='<div class="hint">Providers hold the upstream endpoints + keys — they stay in sbxd; sandboxes only ever see their minted proxy key. Values shown as <code>&lt;masked&gt;</code> are preserved on save. After editing providers run <code>sbxd sync</code> (or the sync button on the burn view).</div>'+
    '<textarea id="cfg-editor" spellcheck="false"></textarea>'+
    '<div class="warnline" id="cfg-warn"></div>';
  fetch('/api/config').then(function(r){return r.json();}).then(function(cfg){
    var ed=$('cfg-editor'); if(ed){ ed.value=JSON.stringify(cfg,null,2); }
  }).catch(function(){});
  var save=addTool($('glob-actions'),'Save',function(){
    var raw=$('cfg-editor').value, j;
    try{ j=JSON.parse(raw); }catch(e){ $('cfg-warn').textContent='not valid JSON: '+e.message; return; }
    void j;
    fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:raw})
      .then(function(r){return r.json();})
      .then(function(res){ if(res.error){ $('cfg-warn').textContent=res.error; } else { $('cfg-warn').textContent=''; toast('config saved — proxy picks it up live','ok'); } });
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
function select(sb){
  state.sel=keyOf(sb);
  state.view='sandbox';                 // leaving any global (burn/config) view
  closeLogs();
  renderList();
  $('empty').hidden=true; $('pane').hidden=false; $('global').hidden=true;
  var vmKind=sb.kind==='vm';
  $('d-name').textContent=sb.name;
  $('d-sub').innerHTML = (vmKind? esc('VM · ') : '')+ esc(sb.kind)+(vmKind && sb.backend? ' · '+esc(sb.backend):'')+
     (sb.project? ' · '+esc(short(sb.project)):'') + (sb.stack? ' · '+esc(sb.stack):'');
  updateButtons(sb);
  renderOverview(sb);
  renderConn(sb);
  if(activeTab==='logs'){ openLogs(sb); }
}
function renderOverview(sb){
  var stat=function(v){return {v:v};};
  var cards=[
    {t:'state', v:pillFor(sb)+' '+esc(sb.status||''), cls:'stat'},
    {t:'image', v:sb.image?esc(sb.image):'<span class="dim">—</span>'},
    {t:'cpu', v: sb.cpuPercent!=null? sb.cpuPercent.toFixed(2)+'%':'—', cls:'stat'},
    {t:'memory', v: sb.memUsage||'—', cls:'stat'},
  ];
  var extra=[];
  cardrowRender($('ov-stats'), cards);
  if(sb.ports && sb.ports.length) extra.push({t:'ports',v:sb.ports.map(esc).join(', ')});
  extra.push({t:'created',v:esc(sb.uptime||sb.createdAt||'—')});
  if(sb.netIO) extra.push({t:'net i/o',v:esc(sb.netIO)});
  if(sb.blockIO) extra.push({t:'disk i/o',v:esc(sb.blockIO)});
  cardrowRender($('ov-cards'), extra);
  s.FetchDetail(sb);
}
function cardrowRender(host, cards){
  host.innerHTML='';
  cards.forEach(function(c){
    var el=h('div',{class:'card'+(c.cls?' '+c.cls:'')},'<h4>'+esc(c.t)+'</h4><div class="v">'+(c.v==null?'—':c.v)+'</div>');
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
        '<div class="hint small">save/restore via CLI: sbxvm snapshot '+esc(sb.name)+' save (stops the VM briefly) · sbxvm snapshot '+esc(sb.name)+' list</div>'});
    } else if(sb.kind==='vm'){
      extras.push({t:'snapshot timeline',v:'<div class="hint small">no snapshots yet — sbxvm snapshot '+esc(sb.name)+' save takes one (offline: the VM stops for the save, then reboots)</div>'});
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
  }).catch(function(){});
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
  box.appendChild(document.createTextNode(line+'\n'));
  while(box.childNodes.length>4000){ box.removeChild(box.firstChild); }
  if($('log-follow').checked){ box.scrollTop=box.scrollHeight; }
}

/* ---------- actions ---------- */
function act(kind,name,what){
  var pkey=kind+':'+name;
  // optimistic UX: the daemon round-trip can take a while (docker's stop
  // grace period, VM ACPI shutdowns) — reflect the intent immediately
  state.pending[pkey]=what;
  var pend=toast((what==='stop'?'stopping ':'starting ')+name+' — waiting for the daemon (stop waits out the grace period; VMs shut down gracefully)…','pend');
  renderList();
  updateButtons(currentSB());
  return fetch('/api/actions/'+what,{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({kind:kind,name:name})}).then(function(r){return r.json();}).then(function(j){
      if(pend) pend.remove();
      delete state.pending[pkey];
      if(j.error){ toast((what==='stop'?'stop':'start')+': '+j.error,'err'); }
      else { toast((what==='start'?'started ':'stopped ')+name+' — daemon confirmed','ok'); }
      updateButtons(currentSB());
      renderList();
    }).catch(function(){
      if(pend) pend.remove();
      delete state.pending[pkey];
      toast(what+' request failed (network) — check daemon connection','err');
      updateButtons(currentSB());
      renderList();
    });
}
function delSB(sb){
  if(!sb) return;
  if(!confirm('Delete sandbox '+sb.name+'? Its container/VM, proxy keys, wallets and sync target are removed.')) return;
  var isVM = sb.kind === 'vm';
  if(isVM && !confirm('Fully delete the VM '+sb.name+'? This wipes the VM disk AND snapshots — irrevocable.')) return;
  return fetch('/api/actions/delete',{method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({kind:sb.kind,name:sb.name,confirm:isVM})}).then(function(r){return r.json();}).then(function(j){
      if(j.error) toast('delete: '+j.error,'err'); else toast('deleted '+sb.name,'ok');
    });
}
function attachGo(sb, mode, spawn){
  if((sb.state||'').toLowerCase()==='booting'){
    toast(sb.name+' is still booting (kit self-registration) — the terminal opens right after the guest finishes booting','err');
    return;
  }
  return fetch('/api/sandboxes/'+sb.kind+'/'+sb.name+'/attach',{
    method:'POST',headers:{'Content-Type':'application/json'},
    body:JSON.stringify({mode:mode,spawn:!!spawn})})
   .then(function(r){return r.json();})
   .then(function(j){
     if(j.error){ toast('attach: '+j.error,'err'); return; }
     var c=j.conn||{};
     if(c.spawnError){ toast('terminal: '+c.spawnError+' — connection copied instead','err'); copy(c.tmuxAttach||c.wtCommand||c.shellCmd||''); return; }
     if(spawn){ toast('Opening Windows Terminal… attach with: '+(c.tmuxAttach||''),'ok'); copy(c.tmuxAttach||''); }
     else { toast('tmux session "'+c.tmuxSession+'" ready — attach: '+(c.tmuxAttach||''),'ok'); copy(c.tmuxAttach||''); }
   });
}

/* ---------- health ---------- */
function refreshHealth(){
  fetch('/api/health').then(function(r){return r.json();}).then(function(j){
    state.health=j; $('ver').textContent='v'+j.version;
    setBadge('h-docker', j.docker); setBadge('h-libvirt', j.libvirt); setBadge('h-tmux', j.tmux);
  }).catch(function(){});
}
function setBadge(id, ok){ var el=$(id); el.textContent=id.replace('h-','')+(ok?' up':' down'); el.className='hbadge '+(ok?'up':'down'); }

/* ---------- SSE inventory ---------- */
function openEvents(){
  if(state.es) state.es.close();
  var es=new EventSource('/api/events');
  state.es=es;
  es.addEventListener('snapshot', function(ev){
    try{ state.list=JSON.parse(ev.data); }catch(e){ return; }
    state.firstData=true;
    renderList();
    if(state.sel){
      var cur=state.list.find(function(x){return keyOf(x)===state.sel;});
      if(cur){
        updateButtons(cur);
        if(activeTab==='overview') renderOverview(cur);
      }
    }
  });
  es.onerror=function(){ /* EventSource auto-reconnects */
    if(!state.firstData){
      Array.prototype.forEach.call(document.querySelectorAll('.fspin-msg'),function(m){
        m.textContent='connection hiccup — retrying…';
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
      Array.prototype.forEach.call(document.querySelectorAll('.tab'),function(x){x.classList.toggle('active',x===t);});
      ['overview','logs','conn','burn'].forEach(function(id){ $('tab-'+id).hidden=(id!==activeTab); });
      if(activeTab==='logs' && state.sel){ var sb=currentSB(); if(sb) openLogs(sb); }
      if(activeTab==='burn'){ var sb2=currentSB(); renderBurnTab(sb2); }
      if(activeTab!=='logs'){ closeLogs(); }
    });
  });
  $('log-clear').addEventListener('click', function(){ $('logbox').textContent=''; });
}
function currentSB(){
  return state.list.find(function(x){return keyOf(x)===state.sel;}) || null;
}

/* ---------- wiring ---------- */
function init(){
  initTabs(); refreshHealth(); openEvents();
  renderList(); // paint the loading spinner right away — the first snapshot may take seconds
  setInterval(refreshHealth, 12000);
  // patience hint while the very first inventory scan is still running
  setTimeout(function(){
    if(!state.firstData){
      Array.prototype.forEach.call(document.querySelectorAll('.fspin-msg'),function(m){
        m.textContent='still looking — initial docker/virsh scans can take a moment…';
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
  $('lnk-help').addEventListener('click', function(ev){ ev.preventDefault(); toast('CLI: sbxd ps | sbxd attach NAME | sbxd logs NAME | sbxd start/stop NAME | sbxd usage [NAME] | sbxd key mint NAME | sbxd sync NAME','ok'); });
  $('lnk-burn').addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('burn'); });
  var lc=$('lnk-create');
  if(lc){ lc.addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('create'); }); }
  Array.prototype.forEach.call(document.querySelectorAll('[data-create]'), function(el){
    el.addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('create'); });
  });
  $('lnk-config').addEventListener('click', function(ev){ ev.preventDefault(); showGlobal('config'); });
}
document.addEventListener('DOMContentLoaded', init);
`
