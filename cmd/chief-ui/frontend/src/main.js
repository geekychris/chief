import './style.css';
import './app.css';

import {
  Ping, ListProjects, ListBacklog, GetTask,
  AddProject, RescanProject, ReadProjectFile,
  AddTask, CmuxCandidates, CmuxBind, SendTask,
  GetProjectSessions, RevealInFinder, OpenPath, OpenURL,
  OpenClaudeTrace, OpenClaudeTraceForProject,
  InstallAnalyzer,
} from '../wailsjs/go/main/App';

// -------- state --------

const state = {
  projects: [],          // ProjectSummary[]
  selectedProject: '',   // '' = All backlogs
  statusFilter: 'pending',
  tasks: [],             // BacklogRow[]
  selectedTaskId: '',
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
  // Modal: add-task
  modalAdd: $('modal-add'),
  addTitle: $('add-title'),
  addCategory: $('add-category'),
  addPriority: $('add-priority'),
  addBody: $('add-body'),
  btnAddSave: $('btn-add-save'),
  btnAddCancel: $('btn-add-cancel'),
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
els.btnAddSave.addEventListener('click', submitAddTask);
els.btnAddCancel.addEventListener('click', () => els.modalAdd.classList.add('hidden'));
els.btnCmuxCancel.addEventListener('click', () => els.modalCmux.classList.add('hidden'));
els.btnOpenTrace.addEventListener('click', openTraceForCurrentProject);
els.btnOpenSessionsDir.addEventListener('click', () => {
  if (currentSessions?.sessions_dir) OpenPath(currentSessions.sessions_dir);
});
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
    els.backlogBody.innerHTML = `<tr><td colspan="6" class="empty">no tasks match</td></tr>`;
    return;
  }
  els.backlogBody.innerHTML = state.tasks.map(t => {
    const glyph = statusGlyph(t.status);
    const sel = t.id === state.selectedTaskId ? ' selected' : '';
    const titleTruncated = t.title.length > 80 ? t.title.slice(0, 77) + '…' : t.title;
    return `
      <tr class="${sel}" data-id="${escapeHtml(t.id)}">
        <td class="col-status st-${t.status}">${glyph}</td>
        <td class="col-id">${escapeHtml(t.id)}</td>
        <td class="col-prio">${t.priority || 0}</td>
        <td class="col-project">${escapeHtml(t.project_name || '')}</td>
        <td class="col-category">${escapeHtml(t.category || '')}</td>
        <td class="col-title">${escapeHtml(titleTruncated)}</td>
      </tr>
    `;
  }).join('');
  els.backlogBody.querySelectorAll('tr').forEach(tr => {
    tr.addEventListener('click', () => showTaskDetail(tr.dataset.id));
  });
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
