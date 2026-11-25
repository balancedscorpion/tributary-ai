import type { SandboxedHtmlArtifact } from "@/types";
import {
  SANDBOXED_HTML_NETWORK_POLICY,
  SANDBOXED_HTML_RENDER_MODE,
  SANDBOXED_HTML_SANDBOX_POLICY,
} from "@/types";

// Synthetic generated-HTML dashboards for /dev/audit/dashboard. Everything is
// self-contained (inline CSS/JS only) because SandboxedHtmlRenderer injects a
// CSP that blocks every network fetch (default-src 'none').

const now = Date.now();

export interface DashboardSample {
  /** Stable id used for artifact ids + switcher keys. */
  key: string;
  /** Short label for the sample switcher chips. */
  label: string;
  artifact: SandboxedHtmlArtifact;
  /** Natural design width of the generated document, for scaled thumbnails. */
  designWidth: number;
  /** Section outline the generator reported alongside the HTML. */
  sections: string[];
  generatedAtMs: number;
  toolName: string;
  durationMs: number;
  dataSource?: {
    title: string;
    rowCount: number;
    columnCount: number;
  };
}

const FONT_STACK =
  "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif";

// ---------------------------------------------------------------------------
// Sample A — Revenue Pulse (light theme, data-bound, interactive range tabs)
// ---------------------------------------------------------------------------

const REVENUE_PULSE_HTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root {
    --ink: #1a1d23; --muted: #69707a; --line: #e4e7ec;
    --bg: #f6f7f9; --card: #ffffff; --accent: #e8590c;
    --good: #0f7b4d; --bad: #c92a2a; --soft: #fff1e6;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: ${FONT_STACK}; background: var(--bg); color: var(--ink); padding: 24px; font-size: 14px; }
  .top { display: flex; align-items: flex-end; justify-content: space-between; margin-bottom: 18px; }
  .top h1 { font-size: 20px; font-weight: 700; letter-spacing: -0.01em; }
  .top .sub { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .tabs { display: inline-flex; background: #fff; border: 1px solid var(--line); border-radius: 8px; padding: 3px; gap: 2px; }
  .tabs button { border: 0; background: transparent; color: var(--muted); font: inherit; font-size: 12px; font-weight: 600; padding: 5px 12px; border-radius: 6px; cursor: pointer; }
  .tabs button.on { background: var(--soft); color: var(--accent); }
  .kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 14px; }
  .card { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; }
  .kpi .label { color: var(--muted); font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
  .kpi .value { font-size: 24px; font-weight: 700; margin-top: 6px; letter-spacing: -0.02em; }
  .kpi .delta { font-size: 12px; font-weight: 600; margin-top: 4px; }
  .delta.up { color: var(--good); } .delta.down { color: var(--bad); }
  .grid2 { display: grid; grid-template-columns: 5fr 7fr; gap: 12px; margin-bottom: 14px; }
  h2 { font-size: 13px; font-weight: 700; margin-bottom: 12px; }
  .bar-row { display: grid; grid-template-columns: 110px 1fr 48px; align-items: center; gap: 10px; margin-bottom: 9px; font-size: 12px; }
  .bar-row .name { color: var(--muted); }
  .bar-track { height: 8px; background: #eef0f3; border-radius: 4px; overflow: hidden; }
  .bar-fill { height: 100%; background: linear-gradient(90deg, #f08c00, var(--accent)); border-radius: 4px; transition: width 240ms ease; }
  .bar-row .num { text-align: right; font-variant-numeric: tabular-nums; color: var(--ink); font-weight: 600; }
  .chart svg { display: block; width: 100%; height: 168px; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  th { text-align: left; color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; padding: 0 10px 8px; border-bottom: 1px solid var(--line); }
  th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; }
  td { padding: 8px 10px; border-bottom: 1px solid #f0f2f5; }
  tr:last-child td { border-bottom: 0; }
  .pill { display: inline-block; padding: 2px 8px; border-radius: 99px; font-size: 11px; font-weight: 600; }
  .pill.good { background: #e6f4ee; color: var(--good); }
  .pill.flat { background: #f1f3f5; color: var(--muted); }
  .foot { color: var(--muted); font-size: 11px; margin-top: 14px; }
</style>
</head>
<body>
  <div class="top">
    <div>
      <h1>Revenue Pulse</h1>
      <div class="sub">analytics-prod &middot; events.sessions_daily &middot; refreshed hourly</div>
    </div>
    <div class="tabs" role="tablist">
      <button data-range="7d">7D</button>
      <button data-range="30d" class="on">30D</button>
      <button data-range="90d">90D</button>
    </div>
  </div>

  <div class="kpis">
    <div class="card kpi"><div class="label">Revenue</div><div class="value" id="k-rev"></div><div class="delta" id="d-rev"></div></div>
    <div class="card kpi"><div class="label">Sessions</div><div class="value" id="k-ses"></div><div class="delta" id="d-ses"></div></div>
    <div class="card kpi"><div class="label">Conversion</div><div class="value" id="k-cvr"></div><div class="delta" id="d-cvr"></div></div>
    <div class="card kpi"><div class="label">Avg order value</div><div class="value" id="k-aov"></div><div class="delta" id="d-aov"></div></div>
  </div>

  <div class="grid2">
    <div class="card">
      <h2>Channel mix &mdash; sessions</h2>
      <div id="bars"></div>
    </div>
    <div class="card chart">
      <h2>Daily revenue</h2>
      <svg viewBox="0 0 560 170" preserveAspectRatio="none" aria-hidden="true">
        <line x1="0" y1="42" x2="560" y2="42" stroke="#eef0f3"/>
        <line x1="0" y1="84" x2="560" y2="84" stroke="#eef0f3"/>
        <line x1="0" y1="126" x2="560" y2="126" stroke="#eef0f3"/>
        <path id="area" fill="rgba(232,89,12,0.10)" stroke="none"/>
        <path id="trend" fill="none" stroke="#e8590c" stroke-width="2.5" stroke-linejoin="round" stroke-linecap="round"/>
      </svg>
    </div>
  </div>

  <div class="card">
    <h2>Top countries</h2>
    <table>
      <thead><tr><th>Country</th><th class="num">Sessions</th><th class="num">Revenue</th><th class="num">Conv.</th><th>Trend</th></tr></thead>
      <tbody>
        <tr><td>United States</td><td class="num">184,201</td><td class="num">$54,310</td><td class="num">3.21%</td><td><span class="pill good">+8.2%</span></td></tr>
        <tr><td>United Kingdom</td><td class="num">61,887</td><td class="num">$18,442</td><td class="num">2.94%</td><td><span class="pill good">+4.5%</span></td></tr>
        <tr><td>Germany</td><td class="num">48,213</td><td class="num">$13,205</td><td class="num">2.61%</td><td><span class="pill flat">+0.4%</span></td></tr>
        <tr><td>France</td><td class="num">33,402</td><td class="num">$9,118</td><td class="num">2.48%</td><td><span class="pill good">+2.7%</span></td></tr>
        <tr><td>Brazil</td><td class="num">29,650</td><td class="num">$6,030</td><td class="num">1.92%</td><td><span class="pill flat">-0.3%</span></td></tr>
      </tbody>
    </table>
  </div>
  <div class="foot">Generated from 64 rows &middot; 8 columns &middot; cached result (1.4 GB scanned)</div>

<script>
(function () {
  var RANGES = {
    "7d":  { rev: "$31.2K",  dRev: "+4.1% vs prior week",  ses: "112,408", dSes: "+1.8%", cvr: "2.91%", dCvr: "+0.3pt", aov: "$44.10", dAov: "+0.6%",
             bars: [62, 48, 33, 24, 16, 8],
             line: [120,108,116,96,102,84,90,72,80,66,74,58,64,52,60] },
    "30d": { rev: "$128.4K", dRev: "+12.4% vs prior 30d",  ses: "482,113", dSes: "+3.1%", cvr: "2.84%", dCvr: "-0.2pt", aov: "$46.20", dAov: "+1.9%",
             bars: [72, 58, 41, 33, 22, 12],
             line: [128,118,124,104,112,92,100,82,92,74,84,64,72,56,48] },
    "90d": { rev: "$361.9K", dRev: "+9.6% vs prior 90d",   ses: "1.41M",   dSes: "+5.4%", cvr: "2.72%", dCvr: "+0.1pt", aov: "$45.05", dAov: "+0.8%",
             bars: [68, 61, 45, 30, 26, 14],
             line: [140,132,136,120,126,110,118,100,108,94,100,84,90,76,68] }
  };
  var CHANNELS = ["Organic search", "Paid search", "Direct", "Referral", "Email", "Social"];
  var MAX = [98421, 76110, 54802, 41233, 28907, 15110];

  function setText(id, text) { document.getElementById(id).textContent = text; }
  function setDelta(id, text) {
    var el = document.getElementById(id);
    el.textContent = text;
    el.className = "delta " + (text.charAt(0) === "-" ? "down" : "up");
  }
  function fmt(n) { return n.toLocaleString("en-US"); }

  function renderBars(values) {
    var host = document.getElementById("bars");
    host.innerHTML = "";
    for (var i = 0; i < CHANNELS.length; i++) {
      var row = document.createElement("div");
      row.className = "bar-row";
      var approx = Math.round(MAX[i] * values[i] / 100);
      row.innerHTML =
        '<span class="name">' + CHANNELS[i] + "</span>" +
        '<span class="bar-track"><span class="bar-fill" style="width:' + values[i] + '%"></span></span>' +
        '<span class="num">' + (approx >= 1000 ? Math.round(approx / 100) / 10 + "K" : fmt(approx)) + "</span>";
      host.appendChild(row);
    }
  }

  function renderLine(points) {
    var step = 560 / (points.length - 1);
    var d = "";
    for (var i = 0; i < points.length; i++) {
      d += (i === 0 ? "M" : "L") + (i * step).toFixed(1) + " " + points[i];
    }
    document.getElementById("trend").setAttribute("d", d);
    document.getElementById("area").setAttribute("d", d + " L560 170 L0 170 Z");
  }

  function apply(range) {
    var r = RANGES[range];
    setText("k-rev", r.rev); setDelta("d-rev", r.dRev);
    setText("k-ses", r.ses); setDelta("d-ses", r.dSes);
    setText("k-cvr", r.cvr); setDelta("d-cvr", r.dCvr);
    setText("k-aov", r.aov); setDelta("d-aov", r.dAov);
    renderBars(r.bars);
    renderLine(r.line);
  }

  var tabs = document.querySelectorAll(".tabs button");
  for (var i = 0; i < tabs.length; i++) {
    tabs[i].addEventListener("click", function () {
      for (var j = 0; j < tabs.length; j++) tabs[j].className = "";
      this.className = "on";
      apply(this.getAttribute("data-range"));
    });
  }
  apply("30d");
})();
</script>
</body>
</html>`;

// ---------------------------------------------------------------------------
// Sample B — Ingestion Ops (dark theme, region filter)
// ---------------------------------------------------------------------------

const INGESTION_OPS_HTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root {
    --bg: #0e1116; --card: #151a21; --line: #262d37; --ink: #e6e8eb;
    --muted: #8b94a1; --accent: #f59f00; --good: #37b26c; --warn: #f59f00; --bad: #e5484d;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: ${FONT_STACK}; background: var(--bg); color: var(--ink); padding: 24px; font-size: 14px; }
  .top { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }
  .top h1 { font-size: 19px; font-weight: 700; letter-spacing: -0.01em; }
  .top .sub { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .env { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; font-weight: 700; color: var(--good); background: rgba(55,178,108,0.12); border: 1px solid rgba(55,178,108,0.35); padding: 4px 10px; border-radius: 99px; }
  .dot { width: 7px; height: 7px; border-radius: 99px; background: currentColor; }
  .stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 14px; }
  .card { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; }
  .stat .label { color: var(--muted); font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.05em; }
  .stat .value { font-size: 23px; font-weight: 700; margin-top: 6px; font-variant-numeric: tabular-nums; }
  .stat .hint { font-size: 11.5px; color: var(--muted); margin-top: 4px; }
  .filters { display: flex; gap: 6px; margin-bottom: 12px; }
  .filters button { border: 1px solid var(--line); background: transparent; color: var(--muted); font: inherit; font-size: 12px; font-weight: 600; padding: 5px 11px; border-radius: 7px; cursor: pointer; }
  .filters button.on { border-color: rgba(245,159,0,0.55); color: var(--accent); background: rgba(245,159,0,0.10); }
  h2 { font-size: 13px; font-weight: 700; margin-bottom: 12px; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  th { text-align: left; color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; padding: 0 10px 8px; border-bottom: 1px solid var(--line); }
  th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; }
  td { padding: 9px 10px; border-bottom: 1px solid #1c222b; }
  tr:last-child td { border-bottom: 0; }
  .st { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; font-size: 12px; }
  .st .dot { width: 6px; height: 6px; }
  .st.healthy { color: var(--good); } .st.degraded { color: var(--warn); } .st.failing { color: var(--bad); }
  .budget { display: grid; grid-template-columns: 150px 1fr 52px; align-items: center; gap: 10px; margin-bottom: 10px; font-size: 12px; }
  .budget .name { color: var(--muted); }
  .track { height: 7px; background: #1c222b; border-radius: 4px; overflow: hidden; }
  .fill { height: 100%; border-radius: 4px; }
  .fill.ok { background: var(--good); } .fill.warn { background: var(--warn); } .fill.bad { background: var(--bad); }
  .budget .num { text-align: right; color: var(--ink); font-weight: 600; font-variant-numeric: tabular-nums; }
  .grid2 { display: grid; grid-template-columns: 7fr 5fr; gap: 12px; margin-top: 14px; }
  .feed { list-style: none; font-size: 12.5px; }
  .feed li { display: flex; gap: 10px; padding: 7px 0; border-bottom: 1px solid #1c222b; }
  .feed li:last-child { border-bottom: 0; }
  .feed .t { color: var(--muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
</head>
<body>
  <div class="top">
    <div>
      <h1>Ingestion Ops</h1>
      <div class="sub">streaming-prod &middot; last 60 minutes &middot; 5s granularity</div>
    </div>
    <span class="env"><span class="dot"></span>All systems nominal</span>
  </div>

  <div class="stats">
    <div class="card stat"><div class="label">Latency p50</div><div class="value">38 ms</div><div class="hint">-3 ms vs prior hour</div></div>
    <div class="card stat"><div class="label">Latency p95</div><div class="value">214 ms</div><div class="hint">+11 ms vs prior hour</div></div>
    <div class="card stat"><div class="label">Latency p99</div><div class="value">612 ms</div><div class="hint">stable</div></div>
    <div class="card stat"><div class="label">Error rate</div><div class="value">0.42%</div><div class="hint">budget: 1.00%</div></div>
  </div>

  <div class="card">
    <h2>Pipeline health</h2>
    <div class="filters" id="filters">
      <button data-region="all" class="on">All regions</button>
      <button data-region="us-east-1">us-east-1</button>
      <button data-region="us-west-2">us-west-2</button>
      <button data-region="eu-west-1">eu-west-1</button>
      <button data-region="ap-south-1">ap-south-1</button>
    </div>
    <table>
      <thead><tr><th>Pipeline</th><th>Region</th><th class="num">Throughput</th><th class="num">Lag</th><th class="num">Errors / min</th><th>Status</th></tr></thead>
      <tbody id="rows">
        <tr data-region="us-east-1"><td>events-raw</td><td>us-east-1</td><td class="num">182K rps</td><td class="num">1.2 s</td><td class="num">4</td><td><span class="st healthy"><span class="dot"></span>Healthy</span></td></tr>
        <tr data-region="us-east-1"><td>cdc-orders</td><td>us-east-1</td><td class="num">41K rps</td><td class="num">0.8 s</td><td class="num">0</td><td><span class="st healthy"><span class="dot"></span>Healthy</span></td></tr>
        <tr data-region="us-west-2"><td>events-raw</td><td>us-west-2</td><td class="num">128K rps</td><td class="num">2.1 s</td><td class="num">9</td><td><span class="st healthy"><span class="dot"></span>Healthy</span></td></tr>
        <tr data-region="eu-west-1"><td>events-raw</td><td>eu-west-1</td><td class="num">96K rps</td><td class="num">14.6 s</td><td class="num">31</td><td><span class="st degraded"><span class="dot"></span>Degraded</span></td></tr>
        <tr data-region="eu-west-1"><td>cdc-orders</td><td>eu-west-1</td><td class="num">22K rps</td><td class="num">1.0 s</td><td class="num">1</td><td><span class="st healthy"><span class="dot"></span>Healthy</span></td></tr>
        <tr data-region="ap-south-1"><td>events-raw</td><td>ap-south-1</td><td class="num">54K rps</td><td class="num">3.4 s</td><td class="num">12</td><td><span class="st healthy"><span class="dot"></span>Healthy</span></td></tr>
        <tr data-region="ap-south-1"><td>device-telemetry</td><td>ap-south-1</td><td class="num">208K rps</td><td class="num">42.0 s</td><td class="num">88</td><td><span class="st failing"><span class="dot"></span>Backpressure</span></td></tr>
      </tbody>
    </table>
  </div>

  <div class="grid2">
    <div class="card">
      <h2>Error budget burn (30d)</h2>
      <div class="budget"><span class="name">events-raw</span><span class="track"><span class="fill ok" style="width:24%"></span></span><span class="num">24%</span></div>
      <div class="budget"><span class="name">cdc-orders</span><span class="track"><span class="fill ok" style="width:9%"></span></span><span class="num">9%</span></div>
      <div class="budget"><span class="name">device-telemetry</span><span class="track"><span class="fill bad" style="width:81%"></span></span><span class="num">81%</span></div>
      <div class="budget"><span class="name">search-index</span><span class="track"><span class="fill warn" style="width:57%"></span></span><span class="num">57%</span></div>
    </div>
    <div class="card">
      <h2>Recent events</h2>
      <ul class="feed">
        <li><span class="t">14:21:08</span><span>Backpressure engaged on device-telemetry (ap-south-1)</span></li>
        <li><span class="t">14:14:52</span><span>Consumer group rebalance completed in 4.1 s</span></li>
        <li><span class="t">13:58:30</span><span>eu-west-1 lag breached 10 s threshold</span></li>
        <li><span class="t">13:41:02</span><span>Snapshot compaction finished (2.4 GB reclaimed)</span></li>
      </ul>
    </div>
  </div>

<script>
(function () {
  var buttons = document.querySelectorAll("#filters button");
  var rows = document.querySelectorAll("#rows tr");
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].addEventListener("click", function () {
      for (var j = 0; j < buttons.length; j++) buttons[j].className = "";
      this.className = "on";
      var region = this.getAttribute("data-region");
      for (var k = 0; k < rows.length; k++) {
        var match = region === "all" || rows[k].getAttribute("data-region") === region;
        rows[k].style.display = match ? "" : "none";
      }
    });
  }
})();
</script>
</body>
</html>`;

// ---------------------------------------------------------------------------
// Sample C — Retention Cohorts (light theme, generated heatmap)
// ---------------------------------------------------------------------------

const RETENTION_COHORTS_HTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { --ink: #1a1d23; --muted: #69707a; --line: #e4e7ec; --bg: #f6f7f9; --card: #ffffff; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: ${FONT_STACK}; background: var(--bg); color: var(--ink); padding: 24px; font-size: 14px; }
  h1 { font-size: 19px; font-weight: 700; letter-spacing: -0.01em; }
  .sub { color: var(--muted); font-size: 12px; margin: 4px 0 16px; }
  .card { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 16px; }
  table { width: 100%; border-collapse: separate; border-spacing: 3px; font-size: 12px; }
  th { color: var(--muted); font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; padding: 4px 6px; text-align: center; }
  th.cohort { text-align: left; }
  td.cohort { color: var(--muted); white-space: nowrap; padding: 0 8px; }
  td.cell { text-align: center; border-radius: 5px; padding: 8px 0; font-weight: 600; font-variant-numeric: tabular-nums; min-width: 52px; }
  td.size { color: var(--muted); text-align: right; padding-right: 10px; font-variant-numeric: tabular-nums; }
  .legend { display: flex; align-items: center; gap: 8px; margin-top: 14px; color: var(--muted); font-size: 11.5px; }
  .swatches { display: flex; gap: 2px; }
  .swatches span { width: 22px; height: 10px; border-radius: 2px; display: inline-block; }
</style>
</head>
<body>
  <h1>Weekly Retention Cohorts</h1>
  <div class="sub">Signups grouped by week &middot; % active in each subsequent week &middot; crm.customers x events.sessions_daily</div>
  <div class="card">
    <table id="heatmap"></table>
    <div class="legend">
      <span>0%</span>
      <div class="swatches" id="legend-swatches"></div>
      <span>60%+</span>
    </div>
  </div>

<script>
(function () {
  var cohorts = [
    { label: "Apr 13", size: 4182, weeks: [100, 46, 38, 33, 30, 28, 26, 25] },
    { label: "Apr 20", size: 3954, weeks: [100, 44, 36, 31, 29, 27, 25] },
    { label: "Apr 27", size: 4421, weeks: [100, 48, 41, 35, 32, 30] },
    { label: "May 4",  size: 4760, weeks: [100, 51, 43, 38, 34] },
    { label: "May 11", size: 5012, weeks: [100, 53, 45, 39] },
    { label: "May 18", size: 4877, weeks: [100, 50, 42] },
    { label: "May 25", size: 5240, weeks: [100, 54] },
    { label: "Jun 1",  size: 5491, weeks: [100] }
  ];

  function color(v) {
    if (v >= 100) return { bg: "#1f2937", fg: "#ffffff" };
    var t = Math.max(0, Math.min(1, v / 60));
    var from = [255, 247, 237];
    var to = [232, 89, 12];
    var c = [];
    for (var i = 0; i < 3; i++) c.push(Math.round(from[i] + (to[i] - from[i]) * t));
    return { bg: "rgb(" + c.join(",") + ")", fg: t > 0.55 ? "#ffffff" : "#7c2d12" };
  }

  var table = document.getElementById("heatmap");
  var head = "<tr><th class=\\"cohort\\">Cohort</th><th>Users</th>";
  for (var w = 0; w < 8; w++) head += "<th>W" + w + "</th>";
  head += "</tr>";
  var rows = "";
  for (var i = 0; i < cohorts.length; i++) {
    var c = cohorts[i];
    rows += "<tr><td class=\\"cohort\\">" + c.label + "</td><td class=\\"size\\">" + c.size.toLocaleString("en-US") + "</td>";
    for (var j = 0; j < 8; j++) {
      if (j < c.weeks.length) {
        var col = color(c.weeks[j]);
        rows += "<td class=\\"cell\\" style=\\"background:" + col.bg + ";color:" + col.fg + "\\">" + c.weeks[j] + "%</td>";
      } else {
        rows += "<td></td>";
      }
    }
    rows += "</tr>";
  }
  table.innerHTML = head + rows;

  var swatches = document.getElementById("legend-swatches");
  for (var s = 0; s <= 5; s++) {
    var sw = document.createElement("span");
    sw.style.background = color(s * 12).bg;
    swatches.appendChild(sw);
  }
})();
</script>
</body>
</html>`;

// ---------------------------------------------------------------------------
// Artifact assembly
// ---------------------------------------------------------------------------

interface SampleSpec {
  key: string;
  label: string;
  title: string;
  html: string;
  toolName: string;
  artifactKind: "interactive_html" | "interactive_data_html";
  designWidth: number;
  sections: string[];
  generatedAtMs: number;
  durationMs: number;
  dataSource?: DashboardSample["dataSource"];
}

function buildSample(spec: SampleSpec): DashboardSample {
  const artifact: SandboxedHtmlArtifact = {
    id: `dev-dashboard-${spec.key}`,
    type: "code",
    language: "html",
    title: spec.title,
    content: spec.html,
    metadata: {
      render_mode: SANDBOXED_HTML_RENDER_MODE,
      sandbox: SANDBOXED_HTML_SANDBOX_POLICY,
      network: SANDBOXED_HTML_NETWORK_POLICY,
      tool_name: spec.toolName,
      artifact_kind: spec.artifactKind,
      inline_preview: true,
      byte_size: spec.html.length,
      sections: spec.sections,
      generated_at_ms: spec.generatedAtMs,
      ...(spec.dataSource
        ? {
            data_source_title: spec.dataSource.title,
            data_source_row_count: spec.dataSource.rowCount,
          }
        : {}),
    },
  };
  return {
    key: spec.key,
    label: spec.label,
    artifact,
    designWidth: spec.designWidth,
    sections: spec.sections,
    generatedAtMs: spec.generatedAtMs,
    toolName: spec.toolName,
    durationMs: spec.durationMs,
    dataSource: spec.dataSource,
  };
}

export const DASHBOARD_SAMPLES: DashboardSample[] = [
  buildSample({
    key: "revenue-pulse",
    label: "Revenue Pulse",
    title: "Revenue Pulse — Last 30 Days",
    html: REVENUE_PULSE_HTML,
    toolName: "create_interactive_data_artifact",
    artifactKind: "interactive_data_html",
    designWidth: 1180,
    sections: ["KPI summary", "Channel mix", "Daily revenue trend", "Top countries"],
    generatedAtMs: now - 4 * 60_000,
    durationMs: 1840,
    dataSource: { title: "sessions_daily (30d)", rowCount: 64, columnCount: 8 },
  }),
  buildSample({
    key: "ingestion-ops",
    label: "Ingestion Ops",
    title: "Ingestion Ops — Latency & Errors",
    html: INGESTION_OPS_HTML,
    toolName: "create_interactive_artifact",
    artifactKind: "interactive_html",
    designWidth: 1180,
    sections: ["Latency percentiles", "Pipeline health", "Error budget", "Recent events"],
    generatedAtMs: now - 11 * 60_000,
    durationMs: 2210,
  }),
  buildSample({
    key: "retention-cohorts",
    label: "Retention Cohorts",
    title: "Weekly Retention Cohorts",
    html: RETENTION_COHORTS_HTML,
    toolName: "create_interactive_data_artifact",
    artifactKind: "interactive_data_html",
    designWidth: 1080,
    sections: ["Cohort heatmap", "Legend"],
    generatedAtMs: now - 26 * 60_000,
    durationMs: 1320,
    dataSource: { title: "customers x sessions_daily", rowCount: 8, columnCount: 10 },
  }),
];
