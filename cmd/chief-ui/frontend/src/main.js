import './style.css';
import './app.css';

import {
  Ping, ListProjects, ListBacklog, GetTask,
  AddProject, RescanProject, ReadProjectFile,
  AddTask, UpdateTask, ReorderTask, DeleteTask,
  CmuxCandidates, CmuxBind, SendTask, SendTasksBatch,
  GetProjectSessions, RevealInFinder, OpenPath, OpenURL,
  OpenClaudeTrace, OpenClaudeTraceForProject,
  InstallAnalyzer,
  HistoryViewerStatus, InstallHistoryViewer, OpenHistoryViewer,
  ListFlags, CountOpenFlags, AnswerFlag,
  ApproveNextTask, SkipNextTask, SnoozeNextTask,
  NextUp, StatsDetailed,
  CodeGraphStatus, InstallCodeGraph, OpenCodeGraph,
} from '../wailsjs/go/main/App';

// -------- state --------

const state = {
  projects: [],          // ProjectSummary[]
  selectedProject: '',   // '' = All backlogs
  statusFilter: '',      // '' = all — completed rows inline styled distinctly
  tasks: [],             // BacklogRow[]
  selectedTaskId: '',
  batchSelection: new Set(), // task ids the user has checkboxed for batch-send
};

// -------- DOM refs --------

const $ = (id) => document.getElementById(id);
const els = {
  status: $('status'),
  btnRefresh: $('btn-refresh'),
  btnAdd: $('btn-add'),
  btnAddTask: $('btn-add-task'),
  projectList: $('project-list'),
  paneTitle: $('pane-title'),
  statusFilter: $('status-filter'),
  backlogBody: $('backlog-body'),
  projectMeta: $('project-meta'),
  pmPath: $('pm-path'),
  pmState: $('pm-state'),
  pmMode: $('pm-mode'),
  pmCreated: $('pm-created'),
  pmConstitution: $('pm-constitution'),
  pmProjectDoc: $('pm-project-doc'),
  detail: $('detail'),
  detailTitle: $('detail-title'),
  detailBody: $('detail-body'),
  detailClose: $('detail-close'),
  btnSendCmux: $('btn-send-cmux'),
  btnEditTask: $('btn-edit-task'),
  btnDeleteTask: $('btn-delete-task'),
  btnSendSelected: $('btn-send-selected'),
  selectAll: $('select-all'),
  // Modal: add-task
  modalAdd: $('modal-add'),
  addTitle: $('add-title'),
  addCategory: $('add-category'),
  addPriority: $('add-priority'),
  addBody: $('add-body'),
  btnAddSave: $('btn-add-save'),
  btnAddCancel: $('btn-add-cancel'),
  // Modal: edit-task
  modalEdit: $('modal-edit'),
  editId: $('edit-id'),
  editTitle: $('edit-title'),
  editCategory: $('edit-category'),
  editPriority: $('edit-priority'),
  editBody: $('edit-body'),
  btnEditSave: $('btn-edit-save'),
  btnEditCancel: $('btn-edit-cancel'),
  // Modal: cmux picker
  modalCmux: $('modal-cmux'),
  cmuxMatches: $('cmux-matches'),
  cmuxMatchesBlock: $('cmux-matches-block'),
  cmuxAll: $('cmux-all'),
  btnCmuxCancel: $('btn-cmux-cancel'),
  // Sessions block
  pmSessions: $('pm-sessions'),
  pmSessionsBody: $('pm-sessions-body'),
  pmSessionsHint: $('pm-sessions-hint'),
  btnOpenTrace: $('btn-open-trace'),
  btnOpenSessionsDir: $('btn-open-sessions-dir'),
  // History-viewer block
  btnOpenHistory: $('btn-open-history'),
  pmHistoryHint: $('pm-history-hint'),
  // Code-graph block
  btnOpenCodeGraph: $('btn-open-codegraph'),
  pmCodeGraphHint: $('pm-codegraph-hint'),
  // Modal: install progress
  modalInstall: $('modal-install'),
  installStatus: $('install-status'),
  installStatusText: $('install-status-text'),
  installLog: $('install-log'),
  installTitle: $('install-title'),
  btnInstallStart: $('btn-install-start'),
  btnInstallClose: $('btn-install-close'),
  // Attention inbox
  btnInbox: $('btn-inbox'),
  inboxBadge: $('inbox-badge'),
  modalInbox: $('modal-inbox'),
  inboxList: $('inbox-list'),
  inboxCountLabel: $('inbox-count-label'),
  btnInboxClose: $('btn-inbox-close'),
  // Next Up modal
  btnNextUp: $('btn-next-up'),
  modalNextUp: $('modal-nextup'),
  nextUpList: $('nextup-list'),
  nextUpCountLabel: $('nextup-count-label'),
  btnNextUpClose: $('btn-nextup-close'),
  // Analytics dashboard
  btnStats: $('btn-stats'),
  modalStats: $('modal-stats'),
  statsCharts: $('stats-charts'),
  statsWindowLabel: $('stats-window-label'),
  statsWindowSelect: $('stats-window-select'),
  btnStatsClose: $('btn-stats-close'),
};

// Current sessions payload (set by loadProjectDocs).
let currentSessions = null;

// -------- wiring --------

els.btnRefresh.addEventListener('click', refreshAll);
els.btnAdd.addEventListener('click', addProject);
els.btnAddTask.addEventListener('click', openAddTaskModal);
els.statusFilter.addEventListener('change', (e) => {
  state.statusFilter = e.target.value;
  loadBacklog();
});
els.detailClose.addEventListener('click', () => {
  state.selectedTaskId = '';
  els.detail.classList.add('hidden');
  renderBacklog();
});
els.btnSendCmux.addEventListener('click', () => sendCurrentTaskToCmux());
els.btnEditTask.addEventListener('click', openEditTaskModal);
els.btnDeleteTask.addEventListener('click', deleteCurrentTask);
els.btnSendSelected.addEventListener('click', sendBatchToCmux);
els.selectAll.addEventListener('change', (e) => toggleSelectAll(e.target.checked));
els.btnAddSave.addEventListener('click', submitAddTask);
els.btnAddCancel.addEventListener('click', () => els.modalAdd.classList.add('hidden'));
els.btnEditSave.addEventListener('click', submitEditTask);
els.btnEditCancel.addEventListener('click', () => els.modalEdit.classList.add('hidden'));
els.btnCmuxCancel.addEventListener('click', () => els.modalCmux.classList.add('hidden'));
els.btnOpenTrace.addEventListener('click', openTraceForCurrentProject);
els.btnOpenSessionsDir.addEventListener('click', () => {
  if (currentSessions?.sessions_dir) OpenPath(currentSessions.sessions_dir);
});
els.btnOpenHistory.addEventListener('click', openHistoryForCurrentProject);
els.btnOpenCodeGraph.addEventListener('click', openCodeGraphForCurrentProject);
els.btnInstallStart.addEventListener('click', runAnalyzerInstall);
els.btnInstallClose.addEventListener('click', () => els.modalInstall.classList.add('hidden'));
els.btnInbox.addEventListener('click', openInbox);
els.btnInboxClose.addEventListener('click', () => els.modalInbox.classList.add('hidden'));
els.btnNextUp.addEventListener('click', openNextUp);
els.btnNextUpClose.addEventListener('click', () => els.modalNextUp.classList.add('hidden'));
els.btnStats.addEventListener('click', openStats);
els.btnStatsClose.addEventListener('click', () => els.modalStats.classList.add('hidden'));
els.statsWindowSelect.addEventListener('change', () => { if (!els.modalStats.classList.contains('hidden')) renderStats(); });
document.addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key === 'r') {
    e.preventDefault();
    refreshAll();
  }
  if (e.key === 'Escape' && !els.detail.classList.contains('hidden')) {
    state.selectedTaskId = '';
    els.detail.classList.add('hidden');
    renderBacklog();
  }
});

// Auto-refresh every 5s so live edits show up without clicking.
setInterval(refreshAll, 5000);

// Kick off.
refreshAll();

// -------- actions --------

async function refreshAll() {
  await Promise.all([updateStatus(), loadProjects(), loadBacklog(), refreshInboxBadge()]);
}

// ---- Attention inbox: badge, list, actions.
// Kept isolated so it can be lifted to its own module later.

async function refreshInboxBadge() {
  try {
    const n = await CountOpenFlags();
    if (n > 0) {
      els.inboxBadge.textContent = String(n);
      els.inboxBadge.classList.remove('hidden');
    } else {
      els.inboxBadge.classList.add('hidden');
    }
    // Re-render the list if the modal is open so approvals from CLI/keyboard
    // show up instantly rather than on next open.
    if (!els.modalInbox.classList.contains('hidden')) {
      renderInbox();
    }
  } catch (e) {
    // chiefd down or method missing — silent (status bar already reflects it).
  }
}

async function openInbox() {
  els.modalInbox.classList.remove('hidden');
  await renderInbox();
}

async function renderInbox() {
  els.inboxList.innerHTML = '<div class="empty">Loading…</div>';
  let flags = [];
  try {
    flags = await ListFlags('', true);
  } catch (e) {
    els.inboxList.innerHTML = `<div class="empty">error: ${e}</div>`;
    return;
  }
  els.inboxCountLabel.textContent = flags.length === 0 ? '(nothing)' : `(${flags.length} open)`;
  if (!flags || flags.length === 0) {
    els.inboxList.innerHTML = '<div class="empty">Inbox zero. No unresolved attention flags.</div>';
    return;
  }
  els.inboxList.innerHTML = '';
  for (const f of flags) {
    els.inboxList.appendChild(renderFlagCard(f));
  }
}

function renderFlagCard(f) {
  const card = document.createElement('div');
  card.className = `flag-card urgency-${f.urgency || 'attention'}`;
  card.dataset.flagId = f.id;

  const head = document.createElement('div');
  head.className = 'flag-head';
  head.innerHTML =
    `<span class="flag-project">${escapeHTML(f.project_name || '(unknown)')}</span>` +
    `<span>${escapeHTML(f.urgency)} · ${escapeHTML(f.kind)} · ${relativeTime(f.created_at)}</span>`;
  card.appendChild(head);

  const body = document.createElement('div');
  body.className = 'flag-body';
  body.textContent = f.question || '(no text)';
  card.appendChild(body);

  const actions = document.createElement('div');
  actions.className = 'flag-actions';

  if (f.kind === 'next_task') {
    // Approve/Skip/Snooze buttons for chief-suggested next tasks.
    const approve = document.createElement('button');
    approve.className = 'primary';
    approve.textContent = 'Approve → send to cmux';
    approve.title = 'Inject the suggested task into the project\'s Claude pane';
    approve.addEventListener('click', () => actOnNext('approve', f.id));

    const skip = document.createElement('button');
    skip.textContent = 'Skip';
    skip.addEventListener('click', () => actOnNext('skip', f.id));

    const snoozeInput = document.createElement('input');
    snoozeInput.type = 'text';
    snoozeInput.placeholder = 'snooze mins (e.g. 30)';
    snoozeInput.style.maxWidth = '160px';
    const snoozeBtn = document.createElement('button');
    snoozeBtn.textContent = 'Snooze';
    snoozeBtn.addEventListener('click', () => {
      const m = parseInt(snoozeInput.value, 10);
      if (!m || m <= 0) { showToast('Enter minutes > 0', true); return; }
      actOnNext('snooze', f.id, m);
    });

    actions.append(approve, skip, snoozeInput, snoozeBtn);
  } else {
    // Question flags: text reply → answer.
    const input = document.createElement('input');
    input.type = 'text';
    input.placeholder = 'Reply — press Enter or click Answer';
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') submitAnswer(f.id, input.value);
    });
    const ans = document.createElement('button');
    ans.className = 'primary';
    ans.textContent = 'Answer';
    ans.addEventListener('click', () => submitAnswer(f.id, input.value));

    const dismiss = document.createElement('button');
    dismiss.textContent = 'Dismiss';
    dismiss.title = 'Mark resolved without a reply';
    dismiss.addEventListener('click', async () => {
      try {
        await AnswerFlag(f.id, '', 'dismissed');
        showToast('Dismissed');
        await refreshInboxBadge();
      } catch (e) { showToast(String(e), true); }
    });

    actions.append(input, ans, dismiss);
  }

  card.appendChild(actions);
  return card;
}

async function submitAnswer(flagID, reply) {
  const text = (reply || '').trim();
  if (!text) { showToast('Reply text required', true); return; }
  try {
    await AnswerFlag(flagID, text, 'answered');
    showToast('Answered');
    await refreshInboxBadge();
  } catch (e) {
    showToast(String(e), true);
  }
}

async function actOnNext(kind, flagID, minutes = 0) {
  try {
    if (kind === 'approve') {
      const r = await ApproveNextTask(flagID);
      showToast(`Sent to ${r.surface_ref}`);
    } else if (kind === 'skip') {
      await SkipNextTask(flagID, '');
      showToast('Skipped');
    } else if (kind === 'snooze') {
      const r = await SnoozeNextTask(flagID, '', minutes);
      showToast(`Snoozed until ${new Date(r.until).toLocaleTimeString()}`);
    }
    await refreshInboxBadge();
  } catch (e) {
    showToast(String(e), true);
  }
}

function escapeHTML(s) {
  if (s == null) return '';
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// ---- Analytics dashboard.
// Inline SVG bar charts (no Chart.js CDN — the app is offline-capable
// and I want to keep the bundled dist small). Two chart shapes:
//   - Timeseries: N days of bars with the count above the bar
//   - Categorical: horizontal bars ranked by count

async function openStats() {
  els.modalStats.classList.remove('hidden');
  await renderStats();
}

async function renderStats() {
  els.statsCharts.innerHTML = '<div class="empty">Loading…</div>';
  const days = parseInt(els.statsWindowSelect.value, 10) || 14;
  let data;
  try {
    data = await StatsDetailed(days);
  } catch (e) {
    els.statsCharts.innerHTML = `<div class="empty">error: ${e}</div>`;
    return;
  }
  els.statsWindowLabel.textContent = `(${days}d window)`;
  els.statsCharts.innerHTML = '';
  // 1. Task velocity (rescan.completed events per day)
  els.statsCharts.appendChild(timeseriesChart('Completions per day', data.completions_per_day, 'var(--good)'));
  // 2. Flags raised per day
  els.statsCharts.appendChild(timeseriesChart('Flags raised per day', data.flags_per_day, 'var(--warn)'));
  // 3. Wake-up pokes per day (task.sent events)
  els.statsCharts.appendChild(timeseriesChart('Wake-up pokes per day', data.wake_pokes_per_day, 'var(--accent-2)'));
  // 4. Urgency distribution — categorical
  els.statsCharts.appendChild(categoricalChart('Notification urgency mix', data.urgency_breakdown, {
    urgent: 'var(--bad)', attention: 'var(--warn)', info: 'var(--text-dim)',
  }));
  // 5. Top categories (pending tasks by category)
  els.statsCharts.appendChild(categoricalChart('Top pending categories', data.category_breakdown, null, 8));
  // 6. Per-project activity (pending + done side by side)
  els.statsCharts.appendChild(stackedProjectChart(data.project_pending, data.project_done));
}

function timeseriesChart(title, points, color) {
  const div = document.createElement('div');
  div.className = 'chart';
  const h3 = document.createElement('h3');
  h3.textContent = title;
  div.appendChild(h3);
  if (!points || points.length === 0) {
    div.classList.add('empty-chart');
    div.appendChild(document.createTextNode('(no data in window)'));
    return div;
  }
  const max = Math.max(1, ...points.map(p => p.count));
  const w = 360, h = 130, pad = 8;
  const bw = (w - pad * 2) / points.length;
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  points.forEach((p, i) => {
    const barH = (p.count / max) * (h - pad * 2);
    const x = pad + i * bw;
    const y = h - pad - barH;
    const rect = document.createElementNS(svgNS, 'rect');
    rect.setAttribute('x', x + 1);
    rect.setAttribute('y', y);
    rect.setAttribute('width', Math.max(1, bw - 2));
    rect.setAttribute('height', barH);
    rect.setAttribute('fill', color);
    rect.setAttribute('opacity', p.count > 0 ? '0.9' : '0.15');
    const t = document.createElementNS(svgNS, 'title');
    t.textContent = `${p.bucket}: ${p.count}`;
    rect.appendChild(t);
    svg.appendChild(rect);
  });
  div.appendChild(svg);
  const legend = document.createElement('div');
  legend.className = 'chart-legend';
  legend.innerHTML = `<span class="lbl">${points[0].bucket}</span><span class="lbl">${points[points.length - 1].bucket}</span><span class="lbl">max ${max}</span>`;
  div.appendChild(legend);
  return div;
}

function categoricalChart(title, bucketMap, colorMap, limit) {
  const div = document.createElement('div');
  div.className = 'chart';
  const h3 = document.createElement('h3');
  h3.textContent = title;
  div.appendChild(h3);
  let entries = Object.entries(bucketMap || {});
  entries.sort((a, b) => b[1] - a[1]);
  if (limit) entries = entries.slice(0, limit);
  if (entries.length === 0) {
    div.classList.add('empty-chart');
    div.appendChild(document.createTextNode('(no data)'));
    return div;
  }
  const max = Math.max(1, ...entries.map(e => e[1]));
  const rowH = 20;
  const w = 360, h = rowH * entries.length + 4;
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.style.height = h + 'px';
  entries.forEach(([label, count], i) => {
    const barW = Math.max(2, (count / max) * (w - 140));
    const rect = document.createElementNS(svgNS, 'rect');
    rect.setAttribute('x', 130);
    rect.setAttribute('y', i * rowH + 2);
    rect.setAttribute('width', barW);
    rect.setAttribute('height', rowH - 4);
    rect.setAttribute('fill', (colorMap && colorMap[label]) || 'var(--accent-2)');
    rect.setAttribute('rx', 2);
    svg.appendChild(rect);
    const text = document.createElementNS(svgNS, 'text');
    text.setAttribute('x', 4);
    text.setAttribute('y', i * rowH + rowH / 2 + 4);
    text.setAttribute('fill', 'var(--text)');
    text.setAttribute('font-size', '11');
    text.textContent = label.length > 18 ? label.slice(0, 16) + '…' : label;
    svg.appendChild(text);
    const val = document.createElementNS(svgNS, 'text');
    val.setAttribute('x', 130 + barW + 4);
    val.setAttribute('y', i * rowH + rowH / 2 + 4);
    val.setAttribute('fill', 'var(--text-dim)');
    val.setAttribute('font-size', '11');
    val.textContent = count;
    svg.appendChild(val);
  });
  div.appendChild(svg);
  return div;
}

function stackedProjectChart(pending, done) {
  const div = document.createElement('div');
  div.className = 'chart';
  const h3 = document.createElement('h3');
  h3.textContent = 'Per-project activity';
  div.appendChild(h3);
  const names = new Set([...Object.keys(pending || {}), ...Object.keys(done || {})]);
  if (names.size === 0) {
    div.classList.add('empty-chart');
    div.appendChild(document.createTextNode('(no projects)'));
    return div;
  }
  const rows = Array.from(names).map(n => ({
    name: n, pending: pending[n] || 0, done: done[n] || 0,
  })).sort((a, b) => (b.pending + b.done) - (a.pending + a.done)).slice(0, 8);
  const max = Math.max(1, ...rows.map(r => r.pending + r.done));
  const rowH = 20;
  const w = 360, h = rowH * rows.length + 4;
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${w} ${h}`);
  svg.style.height = h + 'px';
  rows.forEach((r, i) => {
    const total = r.pending + r.done;
    const y = i * rowH + 2;
    const nameText = document.createElementNS(svgNS, 'text');
    nameText.setAttribute('x', 4);
    nameText.setAttribute('y', y + rowH / 2 + 3);
    nameText.setAttribute('fill', 'var(--text)');
    nameText.setAttribute('font-size', '11');
    nameText.textContent = r.name.length > 14 ? r.name.slice(0, 12) + '…' : r.name;
    svg.appendChild(nameText);
    const scale = (w - 130) / max;
    // pending bar (accent color)
    if (r.pending > 0) {
      const rp = document.createElementNS(svgNS, 'rect');
      rp.setAttribute('x', 110);
      rp.setAttribute('y', y);
      rp.setAttribute('width', r.pending * scale);
      rp.setAttribute('height', rowH - 4);
      rp.setAttribute('fill', 'var(--accent)');
      rp.setAttribute('rx', 2);
      const tp = document.createElementNS(svgNS, 'title');
      tp.textContent = `${r.name}: ${r.pending} pending`;
      rp.appendChild(tp);
      svg.appendChild(rp);
    }
    // done bar (good color) — stacked next to pending
    if (r.done > 0) {
      const rd = document.createElementNS(svgNS, 'rect');
      rd.setAttribute('x', 110 + r.pending * scale);
      rd.setAttribute('y', y);
      rd.setAttribute('width', r.done * scale);
      rd.setAttribute('height', rowH - 4);
      rd.setAttribute('fill', 'var(--good)');
      rd.setAttribute('rx', 2);
      const td = document.createElementNS(svgNS, 'title');
      td.textContent = `${r.name}: ${r.done} done`;
      rd.appendChild(td);
      svg.appendChild(rd);
    }
    const val = document.createElementNS(svgNS, 'text');
    val.setAttribute('x', 110 + total * scale + 4);
    val.setAttribute('y', y + rowH / 2 + 4);
    val.setAttribute('fill', 'var(--text-dim)');
    val.setAttribute('font-size', '11');
    val.textContent = `${r.pending}/${r.done}`;
    svg.appendChild(val);
  });
  div.appendChild(svg);
  const legend = document.createElement('div');
  legend.className = 'chart-legend';
  legend.innerHTML = `<span class="lbl"><span class="swatch" style="background:var(--accent)"></span>pending</span><span class="lbl"><span class="swatch" style="background:var(--good)"></span>done</span>`;
  div.appendChild(legend);
  return div;
}

// ---- Next Up: cross-project ranked queue.
// "Which project should I focus on right now?" answered as a top-10
// list ranked by (priority DESC, source_line ASC). One-click Send
// injects the task into that project's bound cmux surface.

async function openNextUp() {
  els.modalNextUp.classList.remove('hidden');
  await renderNextUp();
}

async function renderNextUp() {
  els.nextUpList.innerHTML = '<div class="empty">Loading…</div>';
  let rows = [];
  try {
    rows = await NextUp(10);
  } catch (e) {
    els.nextUpList.innerHTML = `<div class="empty">error: ${e}</div>`;
    return;
  }
  els.nextUpCountLabel.textContent = rows.length === 0
    ? '(nothing pending)'
    : `(top ${rows.length})`;
  if (!rows || rows.length === 0) {
    els.nextUpList.innerHTML = '<div class="empty">No pending tasks anywhere. Add one to a project\'s backlog.md.</div>';
    return;
  }
  els.nextUpList.innerHTML = '';
  rows.forEach((r, i) => {
    els.nextUpList.appendChild(renderNextUpCard(r, i + 1));
  });
}

function renderNextUpCard(row, rank) {
  const card = document.createElement('div');
  // Reuse the inbox card styling with a priority-tinted stripe.
  const urgencyClass = row.priority >= 5 ? 'urgency-urgent'
    : row.priority >= 3 ? 'urgency-attention'
    : 'urgency-info';
  card.className = `flag-card ${urgencyClass}`;

  const head = document.createElement('div');
  head.className = 'flag-head';
  head.innerHTML =
    `<span><span class="rank-num">#${rank}</span> · <span class="flag-project">${escapeHTML(row.project_name || '(unknown)')}</span></span>` +
    `<span>prio ${row.priority} · ${escapeHTML(row.category || 'Backlog')} · id ${escapeHTML(row.id)}</span>`;
  card.appendChild(head);

  const title = document.createElement('div');
  title.className = 'flag-body';
  title.textContent = row.title;
  card.appendChild(title);

  if (row.body) {
    const details = document.createElement('details');
    const summary = document.createElement('summary');
    summary.textContent = 'body';
    summary.style.color = 'var(--text-dim)';
    summary.style.cursor = 'pointer';
    summary.style.fontSize = '11px';
    details.appendChild(summary);
    const pre = document.createElement('pre');
    pre.textContent = row.body;
    pre.style.whiteSpace = 'pre-wrap';
    pre.style.fontSize = '12px';
    pre.style.color = 'var(--text-dim)';
    pre.style.margin = '4px 0 0 0';
    details.appendChild(pre);
    card.appendChild(details);
  }

  const actions = document.createElement('div');
  actions.className = 'flag-actions';
  const sendBtn = document.createElement('button');
  sendBtn.className = 'primary';
  sendBtn.textContent = 'Send → cmux';
  sendBtn.title = 'Inject this task into its project\'s bound cmux Claude pane';
  sendBtn.addEventListener('click', () => sendNextUp(row));
  const openBtn = document.createElement('button');
  openBtn.textContent = 'Open';
  openBtn.title = 'Switch to this project and select the task';
  openBtn.addEventListener('click', async () => {
    els.modalNextUp.classList.add('hidden');
    await selectProject(row.project_id);
    await showTaskDetail(row.id);
  });
  actions.append(sendBtn, openBtn);
  card.appendChild(actions);
  return card;
}

async function sendNextUp(row) {
  // Reuse the single-task send RPC — the same needs_binding path opens
  // the cmux picker if the project isn't bound yet.
  try {
    const res = await SendTask(row.id, '');
    if (res.ok) {
      showToast(`Sent → ${res.surface_ref}`);
      // Optional: refresh the list so a done-transition removes the row.
      await renderNextUp();
      return;
    }
    if (res.needs_binding || res.surface_gone) {
      // Hop the picker over — this reuses openCmuxPicker which sends
      // once a surface is chosen. Persist state so we know which
      // task we're binding for.
      state.selectedTaskId = row.id;
      openCmuxPicker(res.candidates, row.id);
      return;
    }
    showToast('Send failed: ' + (res.error || 'unknown'), true);
  } catch (e) {
    showToast('Send failed: ' + (e.message || e), true);
  }
}

async function updateStatus() {
  try {
    const p = await Ping();
    els.status.textContent = `chiefd: up · v${p.version} · pid ${p.pid}`;
    els.status.classList.add('up');
    els.status.classList.remove('down');
  } catch (e) {
    els.status.textContent = 'chiefd: down';
    els.status.classList.add('down');
    els.status.classList.remove('up');
  }
}

async function loadProjects() {
  try {
    const projs = await ListProjects();
    state.projects = projs || [];
    renderSidebar();
  } catch (e) {
    console.error('ListProjects failed', e);
  }
}

async function loadBacklog() {
  try {
    state.tasks = await ListBacklog(state.selectedProject, state.statusFilter) || [];
  } catch (e) {
    state.tasks = [];
  }
  renderBacklog();
  if (state.selectedProject) {
    await loadProjectDocs(state.selectedProject);
  } else {
    els.projectMeta.classList.add('hidden');
  }
}

async function loadProjectDocs(projectID) {
  const proj = state.projects.find(p => p.id === projectID || p.name === projectID);
  if (!proj) return;
  els.pmPath.textContent = proj.path;
  els.pmState.textContent = proj.state;
  els.pmMode.textContent = proj.spawn_mode;
  els.pmCreated.textContent = proj.created_at;
  els.projectMeta.classList.remove('hidden');
  try {
    const [con, prj, sessions] = await Promise.all([
      ReadProjectFile(projectID, 'constitution.md'),
      ReadProjectFile(projectID, 'PROJECT.md'),
      GetProjectSessions(projectID),
    ]);
    els.pmConstitution.textContent = con && con.trim() ? con : '(no constitution.md)';
    els.pmProjectDoc.textContent = prj && prj.trim() ? prj : '(no PROJECT.md)';
    currentSessions = sessions;
    renderSessions(sessions);
  } catch (e) {
    els.pmConstitution.textContent = '(read error)';
    els.pmProjectDoc.textContent = '(read error)';
    currentSessions = null;
  }
}

function renderSessions(s) {
  if (!s) {
    els.pmSessionsBody.innerHTML = `<tr><td colspan="4" class="empty">—</td></tr>`;
    els.pmSessionsHint.classList.add('hidden');
    return;
  }
  // Install hint if analyzer isn't found.
  if (!s.analyzer_installed) {
    els.pmSessionsHint.classList.remove('hidden');
    els.pmSessionsHint.innerHTML = `Claude Session Analyzer not detected. Click <em>Install analyzer</em> — Chief will clone <a id="analyzer-repo-link" href="#">${escapeHtml(s.analyzer_repo_url)}</a> and build it.`;
    document.getElementById('analyzer-repo-link').addEventListener('click', (ev) => {
      ev.preventDefault();
      OpenURL(s.analyzer_repo_url);
    });
    els.btnOpenTrace.textContent = 'Install analyzer';
  } else {
    els.pmSessionsHint.classList.add('hidden');
    els.pmSessionsHint.textContent = '';
    els.btnOpenTrace.textContent = 'Open in Analyzer';
  }

  if (!s.sessions || s.sessions.length === 0) {
    els.pmSessionsBody.innerHTML = `<tr><td colspan="4" class="empty">no sessions yet under ${escapeHtml(s.sessions_dir)}</td></tr>`;
    return;
  }
  els.pmSessionsBody.innerHTML = s.sessions.map(row => `
    <tr>
      <td title="${escapeHtml(row.path)}">${escapeHtml(row.id.slice(0, 8))}…${escapeHtml(row.id.slice(-4))}</td>
      <td class="col-size">${humanBytes(row.size)}</td>
      <td class="col-mtime" title="${escapeHtml(row.modified)}">${relativeTime(row.modified)}</td>
      <td class="col-act"><button data-path="${escapeHtml(row.path)}" title="Reveal .jsonl in Finder">Reveal</button></td>
    </tr>
  `).join('');
  els.pmSessionsBody.querySelectorAll('button[data-path]').forEach(b => {
    b.addEventListener('click', (ev) => {
      ev.stopPropagation();
      RevealInFinder(b.dataset.path);
    });
  });
}

async function openTraceForCurrentProject() {
  if (!currentSessions) return;
  if (currentSessions.analyzer_installed) {
    // Deep-link straight to the focused project's view in the analyzer.
    // The slug is what the analyzer app expects for --project.
    await OpenClaudeTraceForProject(currentSessions.slug || '', currentSessions.sessions_dir);
  } else {
    openInstallModal();
  }
}

// -------- history_viewer (jump to zsh history filtered by project dir) --------

async function openCodeGraphForCurrentProject() {
  const projectID = state.selectedProject;
  if (!projectID) return;
  try {
    const status = await CodeGraphStatus();
    if (!status.installed) {
      const missing = [];
      if (!status.has_java) missing.push('java 21');
      if (!status.has_maven) missing.push('maven');
      if (!status.has_npm) missing.push('node/npm');
      let msg = 'code_graph_search is not installed. Install now (~2-5min: clones + mvn package)?';
      if (missing.length) {
        msg += `\n\nMissing prereqs: ${missing.join(', ')}. Install first via brew: brew install openjdk@21 maven node`;
      }
      if (!confirm(msg)) return;
      els.pmCodeGraphHint.classList.remove('hidden');
      els.pmCodeGraphHint.textContent = 'Installing code_graph_search (this can take a few minutes)…';
      const r = await InstallCodeGraph();
      if (!r.ok) {
        els.pmCodeGraphHint.textContent = 'Install failed: ' + (r.error || 'unknown') + ' — see console for full log';
        console.error('code_graph_search install log:\n' + r.log);
        return;
      }
      els.pmCodeGraphHint.textContent = `Built ${r.jar_path} in ${Math.round(r.duration_ms / 1000)}s — launching…`;
    } else {
      els.pmCodeGraphHint.classList.add('hidden');
    }
    const open = await OpenCodeGraph(projectID);
    els.pmCodeGraphHint.classList.remove('hidden');
    const state = open.spawned ? 'spawned' : 'reused';
    els.pmCodeGraphHint.textContent = `${state} on ${open.url}  (config: ${open.config_path})`;
  } catch (e) {
    console.error('code graph open failed', e);
    els.pmCodeGraphHint.classList.remove('hidden');
    els.pmCodeGraphHint.textContent = 'Open failed: ' + (e.message || e);
  }
}

async function openHistoryForCurrentProject() {
  const projectID = state.selectedProject;
  if (!projectID) return;
  try {
    const status = await HistoryViewerStatus();
    if (!status.installed) {
      if (!confirm('history_viewer is not installed. Install it now (via brew or source)?')) return;
      els.pmHistoryHint.classList.remove('hidden');
      els.pmHistoryHint.textContent = 'Installing history_viewer…';
      const r = await InstallHistoryViewer();
      if (!r.ok) {
        els.pmHistoryHint.textContent = 'Install failed: ' + (r.error || 'unknown') + ' — see logs (' + Math.round(r.duration_ms / 1000) + 's)';
        console.error('history_viewer install log:\n' + r.log);
        return;
      }
      els.pmHistoryHint.textContent = 'Installed ' + (r.cli_path || '') + ' (' + Math.round(r.duration_ms / 1000) + 's); opening…';
    } else {
      els.pmHistoryHint.classList.add('hidden');
    }
    const open = await OpenHistoryViewer(projectID);
    els.pmHistoryHint.classList.remove('hidden');
    if (open.mode === 'app-navigate') {
      els.pmHistoryHint.textContent = `Redirected running History Viewer.app to ${open.filter_dir}`;
    } else if (open.mode === 'app') {
      els.pmHistoryHint.textContent = `Spawned History Viewer.app filtered by ${open.filter_dir}`;
    } else {
      els.pmHistoryHint.textContent = `Opened ${open.url}  (${open.spawned ? 'spawned new instance' : 'reused running instance'})`;
    }
  } catch (e) {
    console.error('history viewer open failed', e);
    els.pmHistoryHint.classList.remove('hidden');
    els.pmHistoryHint.textContent = 'Failed: ' + (e.message || e);
  }
}

// -------- analyzer install --------

function openInstallModal() {
  els.installLog.textContent = '(not started)';
  els.installStatus.classList.remove('ok', 'err');
  els.installStatusText.textContent = 'Ready — clone + build ct (+.app if wails is available).';
  els.installStatus.querySelector('.spinner').classList.add('hidden');
  els.btnInstallStart.disabled = false;
  els.btnInstallStart.textContent = 'Start install';
  els.modalInstall.classList.remove('hidden');
}

async function runAnalyzerInstall() {
  els.btnInstallStart.disabled = true;
  els.btnInstallStart.textContent = 'Installing…';
  els.installStatus.classList.remove('ok', 'err');
  els.installStatus.querySelector('.spinner').classList.remove('hidden');
  els.installStatusText.textContent = 'Cloning + building… may take up to 60s the first time.';
  els.installLog.textContent = '(running)';
  try {
    const r = await InstallAnalyzer();
    els.installStatus.querySelector('.spinner').classList.add('hidden');
    els.installLog.textContent = r.log || '(no output)';
    if (r.ok) {
      els.installStatus.classList.add('ok');
      const parts = ['✔ installed'];
      if (r.cli_path) parts.push(`CLI: ${r.cli_path}`);
      if (r.app_path) parts.push(`App: ${r.app_path}`);
      els.installStatusText.textContent = parts.join('  ·  ') + `  (${(r.duration_ms/1000).toFixed(1)}s)`;
      els.btnInstallStart.textContent = 'Reinstall (update)';
      els.btnInstallStart.disabled = false;
      // Refresh the sessions block so the Open button becomes usable.
      if (state.selectedProject) await loadProjectDocs(state.selectedProject);
    } else {
      els.installStatus.classList.add('err');
      els.installStatusText.textContent = 'Install failed: ' + (r.error || 'unknown');
      els.btnInstallStart.textContent = 'Retry';
      els.btnInstallStart.disabled = false;
    }
  } catch (e) {
    els.installStatus.querySelector('.spinner').classList.add('hidden');
    els.installStatus.classList.add('err');
    els.installStatusText.textContent = 'RPC failed: ' + (e.message || e);
    els.btnInstallStart.textContent = 'Retry';
    els.btnInstallStart.disabled = false;
  }
}

function humanBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  if (n < 1024 * 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + ' MB';
  return (n / (1024 * 1024 * 1024)).toFixed(2) + ' GB';
}

function relativeTime(iso) {
  if (!iso) return '';
  const then = new Date(iso).getTime();
  const now = Date.now();
  const s = Math.max(0, Math.floor((now - then) / 1000));
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

async function addProject() {
  const path = prompt('Path to project directory (absolute):');
  if (!path) return;
  try {
    const resp = await AddProject(path.trim(), '', '');
    console.log('added', resp);
    await refreshAll();
  } catch (e) {
    alert('Add project failed: ' + (e.message || e));
  }
}

async function showTaskDetail(taskId) {
  state.selectedTaskId = taskId;
  try {
    const resp = await GetTask(taskId);
    const t = resp.task;
    els.detailTitle.textContent = t.title;
    els.detailBody.innerHTML = `
      <dl>
        <dt>ID</dt><dd><code>${escapeHtml(t.id)}</code></dd>
        <dt>Status</dt><dd class="st-${t.status}">${escapeHtml(t.status)}</dd>
        <dt>Project</dt><dd>${escapeHtml(resp.project_name || '')}</dd>
        <dt>Category</dt><dd>${escapeHtml(t.category || '—')}</dd>
        <dt>Priority</dt><dd>${t.priority}</dd>
        ${t.required_resources?.length ? `<dt>Resources</dt><dd>${t.required_resources.map(escapeHtml).join(', ')}</dd>` : ''}
        ${t.due ? `<dt>Due</dt><dd>${escapeHtml(t.due)}</dd>` : ''}
        <dt>Source</dt><dd><code>${escapeHtml(t.source_file)}</code></dd>
        <dt>Created</dt><dd>${escapeHtml(t.created_at)}</dd>
        ${t.completed_at ? `<dt>Done</dt><dd>${escapeHtml(t.completed_at)}</dd>` : ''}
      </dl>
      ${t.body ? `<div><strong>Body:</strong><pre class="body-pre">${escapeHtml(t.body)}</pre></div>` : ''}
    `;
    els.detail.classList.remove('hidden');
    renderBacklog();
  } catch (e) {
    console.error('GetTask failed', e);
  }
}

async function selectProject(projectID) {
  state.selectedProject = projectID;
  state.selectedTaskId = '';
  els.detail.classList.add('hidden');
  renderSidebar();
  const proj = state.projects.find(p => p.id === projectID);
  els.paneTitle.textContent = projectID ? (proj?.name || projectID) : 'All backlogs';
  // "+ Add task" is only meaningful when a specific project is selected.
  if (projectID) {
    els.btnAddTask.classList.remove('hidden');
  } else {
    els.btnAddTask.classList.add('hidden');
  }
  await loadBacklog();
}

// -------- add task modal --------

function openAddTaskModal() {
  if (!state.selectedProject) {
    alert('Select a project first');
    return;
  }
  els.addTitle.value = '';
  els.addCategory.value = '';
  els.addPriority.value = '0';
  els.addBody.value = '';
  els.modalAdd.classList.remove('hidden');
  setTimeout(() => els.addTitle.focus(), 30);
}

async function submitAddTask() {
  const title = els.addTitle.value.trim();
  if (!title) { els.addTitle.focus(); return; }
  const category = els.addCategory.value.trim();
  const priority = parseInt(els.addPriority.value, 10) || 0;
  const body = els.addBody.value;
  try {
    await AddTask(state.selectedProject, title, body, category, priority, []);
    els.modalAdd.classList.add('hidden');
    // Reset fields so a rapid reopen doesn't show stale content —
    // openAddTaskModal also clears, but resetting on close is defensive.
    els.addTitle.value = '';
    els.addCategory.value = '';
    els.addPriority.value = '0';
    els.addBody.value = '';
    await refreshAll();
  } catch (e) {
    alert('Add task failed: ' + (e.message || e));
  }
}

// -------- send to cmux --------

async function sendCurrentTaskToCmux(surfaceOverride = '') {
  if (!state.selectedTaskId) {
    showToast('No task selected', true);
    return;
  }
  try {
    const res = await SendTask(state.selectedTaskId, surfaceOverride);
    if (res.ok) {
      // Toast + brief button flash. Toast is the primary signal because
      // the button flash was easy to miss (bug report: "send doesn't work").
      showToast(`Sent → ${res.surface_ref}`);
      const prev = els.btnSendCmux.textContent;
      els.btnSendCmux.textContent = 'Sent';
      setTimeout(() => { els.btnSendCmux.textContent = prev; }, 2500);
      return;
    }
    if (res.needs_binding || res.surface_gone) {
      openCmuxPicker(res.candidates, state.selectedTaskId);
      return;
    }
    showToast('Send failed: ' + (res.error || 'unknown'), true);
  } catch (e) {
    showToast('Send failed: ' + (e.message || e), true);
  }
}

function openCmuxPicker(cands, taskId) {
  if (!cands) return;
  const matches = cands.cwd_matches || [];
  const all = cands.all_surfaces || [];
  const current = cands.current_surface || '';
  els.cmuxMatchesBlock.style.display = matches.length ? '' : 'none';
  els.cmuxMatches.innerHTML = matches.length
    ? matches.map(s => renderSurfaceRow(s, current)).join('')
    : '';
  els.cmuxAll.innerHTML = all.length
    ? all.map(s => renderSurfaceRow(s, current)).join('')
    : '<li class="empty-hint">no cmux surfaces (is cmux running?)</li>';
  attachSurfacePickers(taskId);
  els.modalCmux.classList.remove('hidden');
}

function renderSurfaceRow(s, currentRef) {
  const cls = s.ref === currentRef ? ' current' : '';
  const badge = s.is_claude
    ? `<span class="sbadge claude">Claude</span>`
    : `<span class="sbadge">${escapeHtml(s.launcher || s.type || '?')}</span>`;
  return `
    <li class="${cls}" data-ref="${escapeHtml(s.ref)}">
      <span class="sref">${escapeHtml(s.ref)}</span>
      <div>
        <span class="stitle">${escapeHtml(s.title || '(no title)')}</span>
        <span class="scwd">${escapeHtml(s.cwd || '')}</span>
      </div>
      ${badge}
    </li>
  `;
}

function attachSurfacePickers(taskId) {
  const handler = async (li) => {
    const ref = li.dataset.ref;
    els.modalCmux.classList.add('hidden');
    await sendCurrentTaskToCmux(ref);
  };
  els.cmuxMatches.querySelectorAll('li').forEach(li => {
    if (li.classList.contains('empty-hint')) return;
    li.addEventListener('click', () => handler(li));
  });
  els.cmuxAll.querySelectorAll('li').forEach(li => {
    if (li.classList.contains('empty-hint')) return;
    li.addEventListener('click', () => handler(li));
  });
}

// -------- render --------

function renderSidebar() {
  const items = [
    { id: '', name: 'All backlogs', pcount: state.projects.reduce((s, p) => s + p.pending_tasks, 0), all: true },
    ...state.projects.map(p => ({
      id: p.id, name: p.name, path: p.path,
      pcount: p.pending_tasks, dcount: p.done_tasks, ncount: p.deferred_tasks,
    })),
  ];
  els.projectList.innerHTML = items.map(it => {
    const sel = it.id === state.selectedProject ? ' selected' : '';
    const all = it.all ? ' all' : '';
    const pathLine = it.path ? `<span class="ppath">${escapeHtml(it.path)}</span>` : '';
    return `
      <li class="${sel}${all}" data-id="${escapeHtml(it.id)}">
        <div>
          <span class="pname">${escapeHtml(it.name)}</span>
          ${pathLine}
        </div>
        <span class="pcount"><span class="pnum">${it.pcount}</span> pending</span>
      </li>
    `;
  }).join('');
  els.projectList.querySelectorAll('li').forEach(li => {
    li.addEventListener('click', () => selectProject(li.dataset.id));
  });
}

function renderBacklog() {
  // Hide dropped items from the "all" view — they're archived; user has to
  // explicitly filter by "dropped" to see them.  Otherwise a fresh drop
  // stays visible (styled dim/strikethrough) and reads as "delete didn't
  // work" even though the row moved to dropped.md.
  const visible = state.statusFilter === ''
    ? (state.tasks || []).filter(t => t.status !== 'dropped')
    : (state.tasks || []);
  if (visible.length === 0) {
    els.backlogBody.innerHTML = `<tr><td colspan="8" class="empty">no tasks match</td></tr>`;
    updateBatchToolbar();
    return;
  }
  // Reorder controls only make sense within a single project and only for
  // non-done items in backlog.md. Precompute per-project neighbours so
  // ↑/↓ can be disabled at section edges.
  const projectView = !!state.selectedProject;
  els.backlogBody.innerHTML = visible.map((t, i) => {
    const glyph = statusGlyph(t.status);
    const sel = t.id === state.selectedTaskId ? ' selected' : '';
    const titleTruncated = t.title.length > 80 ? t.title.slice(0, 77) + '…' : t.title;
    const showReorder = projectView && t.status !== 'done' && t.source_file === 'backlog.md';
    const canBatch = t.status !== 'done' && t.source_file === 'backlog.md';
    const checked = state.batchSelection.has(t.id) ? 'checked' : '';
    let reorderHTML = '';
    if (showReorder) {
      const prev = visible[i - 1];
      const next = visible[i + 1];
      const canUp   = !!prev && prev.status === t.status && (prev.category || '') === (t.category || '') && prev.source_file === 'backlog.md';
      const canDown = !!next && next.status === t.status && (next.category || '') === (t.category || '') && next.source_file === 'backlog.md';
      reorderHTML = `
        <button class="btn-up"   data-id="${escapeHtml(t.id)}" ${canUp   ? '' : 'disabled'} title="Move up">↑</button>
        <button class="btn-down" data-id="${escapeHtml(t.id)}" ${canDown ? '' : 'disabled'} title="Move down">↓</button>
      `;
    }
    return `
      <tr class="${sel}" data-id="${escapeHtml(t.id)}">
        <td class="col-select">${canBatch ? `<input type="checkbox" class="row-check" data-id="${escapeHtml(t.id)}" ${checked}/>` : ''}</td>
        <td class="col-status st-${t.status}">${glyph}</td>
        <td class="col-id">${escapeHtml(t.id)}</td>
        <td class="col-prio">${t.priority || 0}</td>
        <td class="col-project">${escapeHtml(t.project_name || '')}</td>
        <td class="col-category">${escapeHtml(t.category || '')}</td>
        <td class="col-title">${escapeHtml(titleTruncated)}</td>
        <td class="col-reorder">${reorderHTML}</td>
      </tr>
    `;
  }).join('');
  els.backlogBody.querySelectorAll('tr').forEach(tr => {
    tr.addEventListener('click', (ev) => {
      // Ignore clicks on the batch checkbox or the reorder buttons — they
      // shouldn't open the detail drawer.
      if (ev.target.closest('.col-reorder')) return;
      if (ev.target.closest('.col-select')) return;
      showTaskDetail(tr.dataset.id);
    });
  });
  els.backlogBody.querySelectorAll('button.btn-up').forEach(b => {
    b.addEventListener('click', (ev) => { ev.stopPropagation(); reorderTask(b.dataset.id, 'up'); });
  });
  els.backlogBody.querySelectorAll('button.btn-down').forEach(b => {
    b.addEventListener('click', (ev) => { ev.stopPropagation(); reorderTask(b.dataset.id, 'down'); });
  });
  els.backlogBody.querySelectorAll('input.row-check').forEach(cb => {
    cb.addEventListener('change', (ev) => {
      ev.stopPropagation();
      if (cb.checked) state.batchSelection.add(cb.dataset.id);
      else state.batchSelection.delete(cb.dataset.id);
      updateBatchToolbar();
    });
  });
  updateBatchToolbar();
}

// updateBatchToolbar shows/hides "Send N to cmux" based on selection.
// Works across projects: if the selection spans multiple projects, the
// button labels its cross-project split ("Send N to cmux (K projects)")
// and sendBatchToCmux fans out one batch RPC per project group so each
// subset lands on its own project's bound cmux surface.
function updateBatchToolbar() {
  const count = state.batchSelection.size;
  if (count > 0) {
    els.btnSendSelected.classList.remove('hidden');
    const projects = countProjectsInSelection();
    els.btnSendSelected.textContent = projects > 1
      ? `Send ${count} to cmux (${projects} projects)`
      : `Send ${count} to cmux`;
  } else {
    els.btnSendSelected.classList.add('hidden');
  }
  // Update select-all checkbox tri-state.
  const checkable = (state.tasks || []).filter(
    t => t.status !== 'done' && t.status !== 'dropped' && t.source_file === 'backlog.md');
  const selectedInView = checkable.filter(t => state.batchSelection.has(t.id));
  els.selectAll.indeterminate = selectedInView.length > 0 && selectedInView.length < checkable.length;
  els.selectAll.checked = checkable.length > 0 && selectedInView.length === checkable.length;
}

// countProjectsInSelection returns the number of distinct projects
// represented in the current batch selection. Uses whatever we have in
// state.tasks — for the All-backlogs view this is every visible task
// across all projects, so the lookup is complete.
function countProjectsInSelection() {
  const byPid = new Set();
  const idx = Object.fromEntries((state.tasks || []).map(t => [t.id, t.project_id]));
  for (const id of state.batchSelection) {
    const pid = idx[id];
    if (pid) byPid.add(pid);
  }
  return byPid.size;
}

// groupSelectionByProject returns { pid: [taskId, ...] } for every project
// present in the current selection. Task IDs whose row isn't in state.tasks
// are dropped (shouldn't happen, but defensive).
function groupSelectionByProject() {
  const idx = Object.fromEntries((state.tasks || []).map(t => [t.id, t.project_id]));
  const groups = {};
  for (const id of state.batchSelection) {
    const pid = idx[id];
    if (!pid) continue;
    (groups[pid] ||= []).push(id);
  }
  return groups;
}

function toggleSelectAll(check) {
  const checkable = (state.tasks || []).filter(
    t => t.status !== 'done' && t.status !== 'dropped' && t.source_file === 'backlog.md');
  checkable.forEach(t => {
    if (check) state.batchSelection.add(t.id);
    else state.batchSelection.delete(t.id);
  });
  renderBacklog();
}

async function sendBatchToCmux() {
  if (state.batchSelection.size === 0) return;
  const groups = groupSelectionByProject();
  const projectIDs = Object.keys(groups);
  if (projectIDs.length === 0) return;

  // Fan out: one SendTasksBatch call per project. If any group needs a
  // cmux binding (unbound or surface gone) we serialize a picker per
  // such project so the user picks a target for each. Groups that
  // succeed on the first try don't need any interaction.
  const nameByPid = Object.fromEntries((state.projects || []).map(p => [p.id, p.name]));
  const successes = [];
  const failures = [];
  const needsPicker = []; // [{pid, ids, candidates, surface_gone}]

  await Promise.all(projectIDs.map(async (pid) => {
    const ids = groups[pid];
    try {
      const res = await SendTasksBatch(ids, '');
      if (res.ok) {
        successes.push({ pid, count: res.count, surface: res.surface_ref });
      } else if (res.needs_binding || res.surface_gone) {
        needsPicker.push({ pid, ids, candidates: res.candidates, surface_gone: !!res.surface_gone });
      } else {
        failures.push({ pid, error: res.error || 'unknown' });
      }
    } catch (e) {
      failures.push({ pid, error: e.message || String(e) });
    }
  }));

  // Serialize the picker prompts so the user isn't flooded with modals.
  for (const need of needsPicker) {
    const name = nameByPid[need.pid] || need.pid;
    // eslint-disable-next-line no-await-in-loop
    const ref = await promptForCmuxSurface(need.candidates, name, need.surface_gone);
    if (!ref) {
      failures.push({ pid: need.pid, error: 'cancelled — no surface picked' });
      continue;
    }
    try {
      // eslint-disable-next-line no-await-in-loop
      const res = await SendTasksBatch(need.ids, ref);
      if (res.ok) {
        successes.push({ pid: need.pid, count: res.count, surface: res.surface_ref });
      } else {
        failures.push({ pid: need.pid, error: res.error || 'unknown' });
      }
    } catch (e) {
      failures.push({ pid: need.pid, error: e.message || String(e) });
    }
  }

  // Compose a summary toast. On mixed success/failure we still clear the
  // successful IDs so re-clicking Send only retries the failed ones.
  const succeededIDs = new Set(successes.flatMap(s => groups[s.pid] || []));
  for (const id of succeededIDs) state.batchSelection.delete(id);

  const sentCount = successes.reduce((n, s) => n + s.count, 0);
  const surfacesUsed = successes.map(s => `${nameByPid[s.pid] || s.pid} → ${s.surface}`).join(', ');
  if (failures.length === 0) {
    showToast(`Sent ${sentCount} task(s) · ${surfacesUsed}`);
  } else {
    const failText = failures.map(f => `${nameByPid[f.pid] || f.pid}: ${f.error}`).join(' · ');
    if (sentCount > 0) {
      showToast(`Sent ${sentCount} · failed: ${failText}`, true);
    } else {
      showToast(`Send failed: ${failText}`, true);
    }
  }
  updateBatchToolbar();
  await refreshAll();
}

// promptForCmuxSurface renders the picker modal, waits for the user to
// click a surface or cancel, and resolves with the ref (or null on
// cancel). Extracted from the old openCmuxPickerForBatch so batch
// send can await one picker per project.
function promptForCmuxSurface(cands, projectName, surfaceGone) {
  return new Promise((resolve) => {
    if (!cands) { resolve(null); return; }
    // Update modal header to name the project this picker is for.
    const header = els.modalCmux.querySelector('h2');
    if (header) {
      header.textContent = surfaceGone
        ? `Rebind cmux surface for "${projectName}"`
        : `Pick cmux surface for "${projectName}"`;
    }
    const matches = cands.cwd_matches || [];
    const all = cands.all_surfaces || [];
    const current = cands.current_surface || '';
    els.cmuxMatchesBlock.style.display = matches.length ? '' : 'none';
    els.cmuxMatches.innerHTML = matches.length
      ? matches.map(s => renderSurfaceRow(s, current)).join('')
      : '';
    els.cmuxAll.innerHTML = all.length
      ? all.map(s => renderSurfaceRow(s, current)).join('')
      : '<li class="empty-hint">no cmux surfaces (is cmux running?)</li>';

    const cleanup = () => {
      els.modalCmux.classList.add('hidden');
      els.btnCmuxCancel.removeEventListener('click', onCancel);
      // Detach the per-row handlers by replacing innerHTML on close is
      // not enough — we swapped the listeners above so re-open works.
    };
    const onCancel = () => { cleanup(); resolve(null); };
    els.btnCmuxCancel.addEventListener('click', onCancel);

    const attach = (li) => li.addEventListener('click', () => {
      const ref = li.dataset.ref;
      cleanup();
      resolve(ref);
    });
    els.cmuxMatches.querySelectorAll('li').forEach(li => {
      if (li.classList.contains('empty-hint')) return;
      attach(li);
    });
    els.cmuxAll.querySelectorAll('li').forEach(li => {
      if (li.classList.contains('empty-hint')) return;
      attach(li);
    });
    els.modalCmux.classList.remove('hidden');
  });
}

// (openCmuxPickerForBatch was replaced by promptForCmuxSurface, which
//  returns a promise so batch send can await one picker per project.)

async function deleteCurrentTask() {
  if (!state.selectedTaskId) return;
  const t = state.tasks.find(x => x.id === state.selectedTaskId);
  if (!t) return;
  if (t.source_file !== 'backlog.md') {
    alert('Only backlog.md items can be dropped in-app. Historical items in completedlog.md / dropped.md stay as archives.');
    return;
  }
  // No confirm() dialog — we soft-delete (archive to dropped.md), and the
  // toast below offers immediate visual confirmation + a path to recover.
  const droppedId = t.id;
  const droppedTitle = t.title;
  // Optimistic UI: strip the row from local state and hide the detail
  // drawer BEFORE the RPC returns, so the row disappears the instant the
  // Delete button is clicked. Otherwise the fsnotify -> Rescan ->
  // ListBacklog roundtrip can take a beat and read as "delete didn't fire".
  state.selectedTaskId = '';
  state.batchSelection.delete(droppedId);
  els.detail.classList.add('hidden');
  state.tasks = (state.tasks || []).filter(x => x.id !== droppedId);
  renderSidebar();
  renderBacklog();
  showToast(`Dropped ${droppedId} — ${clip(droppedTitle, 60)}. Archived to dropped.md.`);
  try {
    await DeleteTask(droppedId);
    await refreshAll();
  } catch (e) {
    // Rollback: re-refresh authoritative state and surface the error.
    await refreshAll();
    showToast(`Drop failed: ${e.message || e}`, /*isError*/ true);
  }
}

function clip(s, n) {
  s = String(s || '');
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

// showToast displays a small transient status message in the top-right of
// the main pane.  Used for optimistic-UI feedback (drop, batch send, etc.).
function showToast(text, isError = false) {
  let toast = document.getElementById('toast');
  if (!toast) {
    toast = document.createElement('div');
    toast.id = 'toast';
    toast.className = 'toast';
    document.body.appendChild(toast);
  }
  toast.textContent = text;
  toast.classList.toggle('error', !!isError);
  toast.classList.add('show');
  clearTimeout(showToast._t);
  showToast._t = setTimeout(() => toast.classList.remove('show'), isError ? 6000 : 3200);
}

async function reorderTask(taskId, direction) {
  try {
    await ReorderTask(taskId, direction);
    await refreshAll();
  } catch (e) {
    console.error('reorder failed', e);
  }
}

// -------- edit task modal --------

function openEditTaskModal() {
  if (!state.selectedTaskId) return;
  const t = state.tasks.find(x => x.id === state.selectedTaskId);
  if (!t) return;
  if (t.source_file !== 'backlog.md') {
    alert("Only backlog.md items are editable in place — completed items live in completedlog.md and should be edited manually.");
    return;
  }
  els.editId.textContent = t.id;
  els.editTitle.value = t.title || '';
  els.editCategory.value = t.category || '';
  els.editPriority.value = String(t.priority || 0);
  els.editBody.value = t.body || '';
  els.modalEdit.classList.remove('hidden');
  setTimeout(() => els.editTitle.focus(), 30);
}

async function submitEditTask() {
  const taskId = els.editId.textContent;
  const title = els.editTitle.value.trim();
  if (!title) { els.editTitle.focus(); return; }
  const category = els.editCategory.value.trim();
  const priority = parseInt(els.editPriority.value, 10) || 0;
  const body = els.editBody.value;
  try {
    await UpdateTask(taskId, title, body, category, priority, true);
    els.modalEdit.classList.add('hidden');
    await refreshAll();
    // Re-open the detail drawer so the change is immediately visible.
    if (state.selectedTaskId === taskId) {
      await showTaskDetail(taskId);
    }
  } catch (e) {
    alert('Update failed: ' + (e.message || e));
  }
}

function statusGlyph(s) {
  switch (s) {
    case 'pending': return '[ ]';
    case 'active': return '[*]';
    case 'blocked': return '[⚠]';
    case 'deferred': return '[~]';
    case 'done': return '[x]';
    case 'dropped': return '[!]';
    default: return '[?]';
  }
}

function escapeHtml(s) {
  if (s == null) return '';
  return String(s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
