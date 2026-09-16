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
  // Modal: install progress
  modalInstall: $('modal-install'),
  installStatus: $('install-status'),
  installStatusText: $('install-status-text'),
  installLog: $('install-log'),
  installTitle: $('install-title'),
  btnInstallStart: $('btn-install-start'),
  btnInstallClose: $('btn-install-close'),
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
els.btnInstallStart.addEventListener('click', runAnalyzerInstall);
els.btnInstallClose.addEventListener('click', () => els.modalInstall.classList.add('hidden'));
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
  await Promise.all([updateStatus(), loadProjects(), loadBacklog()]);
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
  if (!state.selectedTaskId) return;
  try {
    const res = await SendTask(state.selectedTaskId, surfaceOverride);
    if (res.ok) {
      // Flash a small confirmation on the button.
      const prev = els.btnSendCmux.textContent;
      els.btnSendCmux.textContent = 'Sent → ' + res.surface_ref;
      setTimeout(() => { els.btnSendCmux.textContent = prev; }, 2500);
      return;
    }
    if (res.needs_binding || res.surface_gone) {
      openCmuxPicker(res.candidates, state.selectedTaskId);
      return;
    }
    alert('Send failed: ' + (res.error || 'unknown'));
  } catch (e) {
    alert('Send failed: ' + (e.message || e));
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
  if (!state.tasks || state.tasks.length === 0) {
    els.backlogBody.innerHTML = `<tr><td colspan="8" class="empty">no tasks match</td></tr>`;
    updateBatchToolbar();
    return;
  }
  // Reorder controls only make sense within a single project and only for
  // non-done items in backlog.md. Precompute per-project neighbours so
  // ↑/↓ can be disabled at section edges.
  const projectView = !!state.selectedProject;
  els.backlogBody.innerHTML = state.tasks.map((t, i) => {
    const glyph = statusGlyph(t.status);
    const sel = t.id === state.selectedTaskId ? ' selected' : '';
    const titleTruncated = t.title.length > 80 ? t.title.slice(0, 77) + '…' : t.title;
    const showReorder = projectView && t.status !== 'done' && t.source_file === 'backlog.md';
    const canBatch = t.status !== 'done' && t.source_file === 'backlog.md';
    const checked = state.batchSelection.has(t.id) ? 'checked' : '';
    let reorderHTML = '';
    if (showReorder) {
      const prev = state.tasks[i - 1];
      const next = state.tasks[i + 1];
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

// updateBatchToolbar shows/hides "Send N to cmux" based on selection. The
// batch endpoint requires all tasks to be in one project, so batch is only
// meaningful in a per-project view. The button is hidden otherwise.
function updateBatchToolbar() {
  const count = state.batchSelection.size;
  const projectView = !!state.selectedProject;
  if (count > 0 && projectView) {
    els.btnSendSelected.classList.remove('hidden');
    els.btnSendSelected.textContent = `Send ${count} to cmux`;
  } else {
    els.btnSendSelected.classList.add('hidden');
  }
  // Update select-all checkbox tri-state.
  const checkable = state.tasks.filter(t => t.status !== 'done' && t.source_file === 'backlog.md');
  const selectedInView = checkable.filter(t => state.batchSelection.has(t.id));
  els.selectAll.indeterminate = selectedInView.length > 0 && selectedInView.length < checkable.length;
  els.selectAll.checked = checkable.length > 0 && selectedInView.length === checkable.length;
}

function toggleSelectAll(check) {
  const checkable = state.tasks.filter(t => t.status !== 'done' && t.source_file === 'backlog.md');
  checkable.forEach(t => {
    if (check) state.batchSelection.add(t.id);
    else state.batchSelection.delete(t.id);
  });
  renderBacklog();
}

async function sendBatchToCmux() {
  if (state.batchSelection.size === 0) return;
  const ids = Array.from(state.batchSelection);
  try {
    const res = await SendTasksBatch(ids, '');
    if (res.ok) {
      const orig = els.btnSendSelected.textContent;
      els.btnSendSelected.textContent = `Sent ${res.count} → ${res.surface_ref}`;
      setTimeout(() => { els.btnSendSelected.textContent = orig; }, 2500);
      state.batchSelection.clear();
      await refreshAll();
      return;
    }
    if (res.needs_binding || res.surface_gone) {
      // Reuse the same picker as single-send. When user picks a surface,
      // fire batch again with the override.
      openCmuxPickerForBatch(res.candidates, ids);
      return;
    }
    alert('Batch send failed: ' + (res.error || 'unknown'));
  } catch (e) {
    alert('Batch send failed: ' + (e.message || e));
  }
}

function openCmuxPickerForBatch(cands, taskIds) {
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
  const handler = async (li) => {
    const ref = li.dataset.ref;
    els.modalCmux.classList.add('hidden');
    try {
      const res = await SendTasksBatch(taskIds, ref);
      if (res.ok) {
        state.batchSelection.clear();
        await refreshAll();
      } else {
        alert('Batch send failed: ' + (res.error || 'unknown'));
      }
    } catch (e) {
      alert('Batch send failed: ' + (e.message || e));
    }
  };
  els.cmuxMatches.querySelectorAll('li').forEach(li => {
    if (li.classList.contains('empty-hint')) return;
    li.addEventListener('click', () => handler(li));
  });
  els.cmuxAll.querySelectorAll('li').forEach(li => {
    if (li.classList.contains('empty-hint')) return;
    li.addEventListener('click', () => handler(li));
  });
  els.modalCmux.classList.remove('hidden');
}

async function deleteCurrentTask() {
  if (!state.selectedTaskId) return;
  const t = state.tasks.find(x => x.id === state.selectedTaskId);
  if (!t) return;
  if (t.source_file !== 'backlog.md') {
    alert('Only backlog.md items are deletable in-app. Completed items live in completedlog.md as history.');
    return;
  }
  if (!confirm(`Delete task ${t.id}?\n\n${t.title}\n\nThis removes the line from backlog.md.`)) return;
  try {
    await DeleteTask(t.id);
    state.selectedTaskId = '';
    state.batchSelection.delete(t.id);
    els.detail.classList.add('hidden');
    await refreshAll();
  } catch (e) {
    alert('Delete failed: ' + (e.message || e));
  }
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
    case 'blocked': return '[!]';
    case 'deferred': return '[~]';
    case 'done': return '[x]';
    default: return '[?]';
  }
}

function escapeHtml(s) {
  if (s == null) return '';
  return String(s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}
