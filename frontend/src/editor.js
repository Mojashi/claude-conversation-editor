const go = window._go = window.go?.main?.App || {};
async function inv(cmd, args) {
  // Wails: window.go.main.App.Method(args...)
  if (go) {
    if (cmd === 'list_projects') return go.ListProjects();
    if (cmd === 'list_sessions') return go.ListSessions(args.project_id);
    if (cmd === 'get_conversation') return go.GetConversation(args.project_id, args.session_id);
    if (cmd === 'save_conversation') return go.SaveConversation(args.project_id, args.session_id, args.req);
    if (cmd === 'edit_message') return go.EditMessage(args.project_id, args.session_id, args.uuid, args.new_text);
    if (cmd === 'branch_new_session') return go.BranchNewSession(args.project_id, args.session_id, args.uuid);
    if (cmd === 'restore_sidechain') return go.RestoreSidechain(args.project_id, args.session_id, args.uuid);
    if (cmd === 'summarize_messages') return go.SummarizeMessages(args.project_id, args.session_id, args.uuids);
    if (cmd === 'apply_summary') return go.ApplySummary(args.project_id, args.session_id, args.uuids, args.summary);
    if (cmd === 'idealize_messages') return go.IdealizeMessages(args.project_id, args.session_id, args.uuids);
    if (cmd === 'apply_idealized') return go.ApplyIdealized(args.project_id, args.session_id, args.uuids, args.messages_json);
    if (cmd === 'exec_claude') return go.ExecClaude(args.project_id, args.session_id, args.skip_permissions, args.terminal);
    if (cmd === 'get_available_terminals') return go.GetAvailableTerminals();
    if (cmd === 'get_claude_command') return go.GetClaudeCommand(args.project_id, args.session_id, args.skip_permissions);
    if (cmd === 'insert_message') return go.InsertMessage(args.project_id, args.session_id, args.after_uuid, args.role, args.text);
  }
  // fallback fetch for dev
  const map = {
    list_projects: () => fetch('/api/projects').then(r => r.json()),
    list_sessions: (a) => fetch(`/api/sessions/${a.project_id}`).then(r => r.json()),
    get_conversation: (a) => fetch(`/api/conversation/${a.project_id}/${a.session_id}`).then(r => r.json()),
    save_conversation: (a) => fetch(`/api/conversation/${a.project_id}/${a.session_id}`, {method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({keep_uuids:a.req.keep_uuids})}).then(r => r.json()),
  };
  return map[cmd](args);
}

let currentProject = null;
let currentSession = null;
let allMessages = [];
let deletedUuids = new Set();
let pendingInsertLines = [];
let pendingDeletedUuids = [];
let showTools = true;
let showSidechain = false;
let lastCheckedIndex = null;
let undoStack = [];

function pushUndo() {
  undoStack.push({
    allMessages: JSON.parse(JSON.stringify(allMessages)),
    deletedUuids: new Set(deletedUuids),
    pendingInsertLines: [...pendingInsertLines],
    pendingDeletedUuids: [...pendingDeletedUuids],
  });
  if (undoStack.length > 50) undoStack.shift();
}

function undo() {
  if (undoStack.length === 0) { setStatus('Nothing to undo.'); return; }
  const state = undoStack.pop();
  allMessages = state.allMessages;
  deletedUuids = state.deletedUuids;
  pendingInsertLines = state.pendingInsertLines;
  pendingDeletedUuids = state.pendingDeletedUuids;
  renderConversation();
  updateButtons();
  setStatus(`Undo (${undoStack.length} left)`);
}

document.addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key === 'z') {
    e.preventDefault();
    undo();
  }
});

// Utilities
function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024*1024) return (bytes/1024).toFixed(1) + ' KB';
  return (bytes/1024/1024).toFixed(2) + ' MB';
}

function formatDate(ts) {
  if (!ts) return '';
  const d = new Date(typeof ts === 'number' ? ts*1000 : ts);
  return d.toLocaleString('ja-JP', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

function setStatus(msg) {
  document.getElementById('status-bar').textContent = msg;
}

// Projects
async function loadProjects() {
  const projects = await inv('list_projects');
  const list = document.getElementById('project-list');
  document.getElementById('project-count').textContent = projects.length;

  if (!projects.length) {
    list.innerHTML = '<div class="empty-state">No projects</div>';
    return;
  }

  list.innerHTML = projects.map(p => `
    <div class="list-item" onclick="selectProject('${p.id}', this)" data-id="${p.id}">
      <div class="name" title="${p.name}">${p.name}</div>
      <div class="meta">${p.session_count} sessions · ${formatDate(p.mtime)}</div>
    </div>
  `).join('');
}

async function selectProject(projectId, el) {
  currentProject = projectId;
  currentSession = null;
  document.querySelectorAll('#project-list .list-item').forEach(e => e.classList.remove('active'));
  el.classList.add('active');
  clearConversation();
  await loadSessions(projectId);
}

// Sessions
async function loadSessions(projectId) {
  const list = document.getElementById('session-list');
  list.innerHTML = '<div class="loading">Loading...</div>';

  const sessions = await inv('list_sessions', { project_id: projectId });
  document.getElementById('session-count').textContent = sessions.length;

  if (!sessions.length) {
    list.innerHTML = '<div class="empty-state">No sessions</div>';
    return;
  }

  list.innerHTML = sessions.map(s => `
    <div class="list-item" onclick="selectSession('${s.id}', this)" data-id="${s.id}">
      <div class="name" title="${s.preview}">${s.preview || '(empty)'}</div>
      <div class="meta">${s.msg_count} msgs · ${formatSize(s.size)} · ${formatDate(s.mtime)}</div>
    </div>
  `).join('');
}

async function selectSession(sessionId, el) {
  currentSession = sessionId;
  document.querySelectorAll('#session-list .list-item').forEach(e => e.classList.remove('active'));
  el.classList.add('active');
  await loadConversation(sessionId);
}

// Conversation
function clearConversation() {
  allMessages = [];
  deletedUuids = new Set();
  pendingInsertLines = [];
  pendingDeletedUuids = [];
  document.getElementById('conv-messages').innerHTML = '<div class="empty-state"><div class="icon">💬</div>Select a session</div>';
  document.getElementById('conv-toolbar').style.display = 'none';
  document.getElementById('session-list').innerHTML = '<div class="empty-state"><div class="icon">📂</div>Select a project</div>';
}

async function loadConversation(sessionId) {
  const messages = document.getElementById('conv-messages');
  messages.innerHTML = '<div class="loading">Loading...</div>';
  deletedUuids = new Set();
  pendingInsertLines = [];
  pendingDeletedUuids = [];
  undoStack = [];
  lastCheckedIndex = null;

  const data = await inv('get_conversation', { project_id: currentProject, session_id: sessionId });

  allMessages = data.messages;
  document.getElementById('conv-toolbar').style.display = 'flex';
  renderConversation(data.total_size);
}

function renderConversation(totalSize) {
  const container = document.getElementById('conv-messages');
  if (!allMessages.length) {
    container.innerHTML = '<div class="empty-state">No messages</div>';
    return;
  }

  container.innerHTML = allMessages.map((msg, i) => renderMessage(msg, i)).join('');
  renderHeatmap();
  updateSizeInfo(totalSize);
  updateButtons();
}

function renderMessage(msg, index) {
  if (msg.is_compact_boundary) {
    const meta = msg.compact_meta || {};
    const trigger = meta.trigger || '?';
    const preTokens = meta.preTokens ? `${(meta.preTokens / 1000).toFixed(0)}k tokens` : '';
    const ts = formatDate(msg.timestamp);
    return `
      <div class="compact-boundary" id="msg-${index}" data-uuid="${msg.uuid || ''}" data-index="${index}">
        <div class="compact-line"></div>
        <div class="compact-label">
          ⚡ Compaction
          <span class="compact-tokens">${trigger}${preTokens ? ' · ' + preTokens : ''}${ts ? ' · ' + ts : ''}</span>
        </div>
        <div class="compact-line"></div>
      </div>
    `;
  }

  const isDeleted = deletedUuids.has(msg.uuid);
  const isSide = msg.isSidechain;
  const cs = msg.content_summary;

  const typeTags = cs.types.map(t => `<span class="type-tag ${t}">${t}</span>`).join('');

  let contentHtml = '';
  const rawContent = msg.raw?.message?.content;

  if (Array.isArray(rawContent)) {
    contentHtml = rawContent.map(c => renderContentBlock(c)).join('');
  } else if (typeof rawContent === 'string') {
    const trimmed = rawContent.replace(/<[^>]+>/g, '').trim();
    if (trimmed) contentHtml = `<div class="msg-text">${escHtml(trimmed.slice(0, 2000))}</div>`;
  }

  const usageHtml = msg.usage ? `<span class="msg-size">${(msg.usage.input_tokens || 0) + (msg.usage.output_tokens || 0)} tok</span>` : `<span class="msg-size">${formatSize(cs.size)}</span>`;

  return `
    <div class="message ${isDeleted ? 'deleted-msg' : ''}" id="msg-${index}"
         data-uuid="${msg.uuid || ''}" data-index="${index}">
      <input type="checkbox" class="msg-checkbox" ${isDeleted ? 'checked' : ''}
             onclick="handleCheckboxClick(event, ${index})" />
      <div class="msg-body">
        <div class="msg-card role-${msg.role} ${isDeleted ? 'selected' : ''} ${isSide ? 'sidechain' : ''}">
          <div class="msg-header" onclick="toggleContent(${index})">
            <span class="role-badge">${msg.role}</span>
            <span class="msg-types">${typeTags}</span>
            ${usageHtml}
            <span class="msg-ts">${formatDate(msg.timestamp)}</span>
            <button class="truncate-btn" onclick="event.stopPropagation(); truncateFrom(${index})" title="Keep only up to this message">✂ Truncate after</button>
            <button class="edit-btn" onclick="event.stopPropagation(); startEdit(${index})" title="Edit message content">✏ Edit</button>
            <button class="insert-btn" onclick="event.stopPropagation(); showInsertDialog(${index})" title="Insert message after this one">+ Insert</button>
            <button class="branch-btn" onclick="event.stopPropagation(); branchFrom(${index})" title="Create new session branching from this message">⎇ Branch</button>
          </div>
          <div class="msg-content" id="content-${index}">
            ${contentHtml || `<span style="color:#484f58">— no text content —</span>`}
          </div>
        </div>
      </div>
    </div>
  `;
}

function renderContentBlock(c) {
  const type = c.type;
  if (type === 'text') {
    const text = c.text || '';
    return `<div class="msg-text">${escHtml(text.slice(0, 3000))}${text.length > 3000 ? '\n… (truncated)' : ''}</div>`;
  }
  if (type === 'thinking') {
    return `<div class="tool-detail"><div class="tool-name">💭 thinking</div><div class="tool-body">${escHtml((c.thinking||'').slice(0, 500))}…</div></div>`;
  }
  if (type === 'tool_use') {
    const input = JSON.stringify(c.input || {}, null, 2);
    return `<div class="tool-detail"><div class="tool-name">🔧 ${escHtml(c.name || 'tool')}</div><div class="tool-body">${escHtml(input.slice(0, 500))}${input.length > 500 ? '\n…' : ''}</div></div>`;
  }
  if (type === 'tool_result') {
    const isErr = c.is_error;
    const content = Array.isArray(c.content)
      ? c.content.map(x => x.text || '').join('\n')
      : (c.content || '');
    return `<div class="tool-detail"><div class="tool-name ${isErr ? 'tool-result-err' : 'tool-result-ok'}">${isErr ? '❌' : '✅'} result</div><div class="tool-body">${escHtml(content.slice(0, 500))}${content.length > 500 ? '\n…' : ''}</div></div>`;
  }
  return `<div class="tool-detail"><div class="tool-body">${escHtml(JSON.stringify(c).slice(0, 200))}</div></div>`;
}

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function toggleContent(index) {
  const el = document.getElementById(`content-${index}`);
  el.classList.toggle('collapsed');
}

// Selection
function handleCheckboxClick(event, index) {
  pushUndo();
  const checked = event.target.checked;
  if (event.shiftKey && lastCheckedIndex !== null) {
    const from = Math.min(lastCheckedIndex, index);
    const to = Math.max(lastCheckedIndex, index);
    for (let i = from; i <= to; i++) {
      setMessageChecked(i, checked);
    }
  } else {
    setMessageChecked(index, checked);
  }
  lastCheckedIndex = index;
  updateSizeInfo();
  updateButtons();
}

function setMessageChecked(index, checked) {
  const msg = allMessages[index];
  if (!msg) return;
  if (checked) {
    deletedUuids.add(msg.uuid);
  } else {
    deletedUuids.delete(msg.uuid);
  }
  const cb = document.querySelector(`#msg-${index} .msg-checkbox`);
  if (cb) cb.checked = checked;
  const card = document.querySelector(`#msg-${index} .msg-card`);
  if (card) card.classList.toggle('selected', checked);
}

function toggleMessage(index, checked) {
  setMessageChecked(index, checked);
  updateSizeInfo();
  updateButtons();
}

function truncateFrom(index) {
  pushUndo();
  // Delete all messages from index onwards
  for (let i = index; i < allMessages.length; i++) {
    deletedUuids.add(allMessages[i].uuid);
    const cb = document.querySelector(`#msg-${i} .msg-checkbox`);
    if (cb) cb.checked = true;
    const card = document.querySelector(`#msg-${i} .msg-card`);
    if (card) card.classList.add('selected');
  }
  updateSizeInfo();
  updateButtons();
}

function selectAll(checked) {
  pushUndo();
  allMessages.forEach((msg, i) => {
    if (msg.is_compact_boundary) return;
    setMessageChecked(i, checked);
  });
  updateSizeInfo();
  updateButtons();
}

function toggleTools() {
  showTools = !showTools;
  allMessages.forEach((msg, i) => {
    if (msg.is_tool_only) {
      const el = document.getElementById(`msg-${i}`);
      if (el) el.classList.toggle('hidden', !showTools);
    }
  });
}

function toggleSidechain() {
  showSidechain = !showSidechain;
  allMessages.forEach((msg, i) => {
    if (msg.isSidechain) {
      const el = document.getElementById(`msg-${i}`);
      if (el) el.classList.toggle('hidden', !showSidechain);
    }
  });
}

function startEdit(index) {
  const msg = allMessages[index];
  if (!msg) return;
  const contentEl = document.getElementById(`content-${index}`);

  // Extract current text
  const rawContent = msg.raw?.message?.content;
  let currentText = '';
  if (typeof rawContent === 'string') {
    currentText = rawContent.replace(/<[^>]+>/g, '').trim();
  } else if (Array.isArray(rawContent)) {
    currentText = rawContent.filter(c => c.type === 'text').map(c => c.text || '').join('\n');
  }

  contentEl.innerHTML = `
    <textarea class="edit-area" id="edit-ta-${index}">${escHtml(currentText)}</textarea>
    <div class="edit-actions">
      <button class="btn btn-success" onclick="commitEdit(${index})">Save</button>
      <button class="btn btn-default" onclick="cancelEdit(${index})">Cancel</button>
    </div>
  `;
  document.getElementById(`edit-ta-${index}`).focus();
}

function cancelEdit(index) {
  // Re-render the original content
  const msg = allMessages[index];
  const contentEl = document.getElementById(`content-${index}`);
  const rawContent = msg.raw?.message?.content;
  let contentHtml = '';
  if (Array.isArray(rawContent)) {
    contentHtml = rawContent.map(c => renderContentBlock(c)).join('');
  } else if (typeof rawContent === 'string') {
    const trimmed = rawContent.replace(/<[^>]+>/g, '').trim();
    if (trimmed) contentHtml = `<div class="msg-text">${escHtml(trimmed.slice(0, 2000))}</div>`;
  }
  contentEl.innerHTML = contentHtml || `<span style="color:#484f58">— no text content —</span>`;
}

async function commitEdit(index) {
  const msg = allMessages[index];
  const ta = document.getElementById(`edit-ta-${index}`);
  if (!ta) return;
  const newText = ta.value;

  setStatus('Saving edit...');
  try {
    await inv('edit_message', {
      project_id: currentProject,
      session_id: currentSession,
      uuid: msg.uuid,
      new_text: newText,
    });
    // Update local state
    const raw = msg.raw;
    if (typeof raw?.message?.content === 'string') {
      msg.raw.message.content = newText;
    } else if (Array.isArray(raw?.message?.content)) {
      const arr = raw.message.content;
      let first = true;
      for (const c of arr) {
        if (c.type === 'text') {
          c.text = first ? newText : '';
          first = false;
        }
      }
    }
    cancelEdit(index);
    setStatus('Edit saved.');
  } catch(e) {
    setStatus('Edit failed: ' + e);
  }
}

let insertAfterIndex = null;

function showInsertDialog(index) {
  insertAfterIndex = index;
  document.getElementById('insert-ta').value = '';
  document.querySelector('input[name="insert-role"][value="user"]').checked = true;
  document.getElementById('insert-overlay').classList.add('show');
  setTimeout(() => document.getElementById('insert-ta').focus(), 50);
}

function hideInsertDialog(e) {
  if (e && e.target !== document.getElementById('insert-overlay')) return;
  document.getElementById('insert-overlay').classList.remove('show');
}

async function commitInsert() {
  const text = document.getElementById('insert-ta').value.trim();
  if (!text) return;
  const role = document.querySelector('input[name="insert-role"]:checked').value;
  const msg = allMessages[insertAfterIndex];
  hideInsertDialog();
  setStatus('Inserting message...');
  try {
    await inv('insert_message', {
      project_id: currentProject,
      session_id: currentSession,
      after_uuid: msg.uuid,
      role,
      text,
    });
    setStatus('Message inserted. Reloading...');
    await loadConversation(currentSession);
  } catch(e) {
    setStatus('Insert failed: ' + e);
  }
}

async function showExecDialog() {
  if (!currentProject) return;
  const path = '/' + currentProject.replace(/-/g, '/').replace(/^\//, '');
  document.getElementById('exec-path').textContent = path;
  document.getElementById('exec-skip-perms').checked = false;

  // populate terminal dropdown
  const sel = document.getElementById('exec-terminal');
  sel.innerHTML = '';
  try {
    const terminals = await inv('get_available_terminals');
    terminals.forEach(t => {
      const opt = document.createElement('option');
      opt.value = t; opt.textContent = t;
      sel.appendChild(opt);
    });
  } catch(_) {
    const opt = document.createElement('option');
    opt.value = 'Terminal'; opt.textContent = 'Terminal';
    sel.appendChild(opt);
  }

  updateExecCommand();
  document.getElementById('exec-overlay').classList.add('show');
}

async function updateExecCommand() {
  const skip = document.getElementById('exec-skip-perms').checked;
  try {
    const cmd = await inv('get_claude_command', { project_id: currentProject, session_id: currentSession || '', skip_permissions: skip });
    document.getElementById('exec-cmd-preview').textContent = cmd;
  } catch(_) {}
}

function copyExecCommand() {
  const cmd = document.getElementById('exec-cmd-preview').textContent;
  navigator.clipboard.writeText(cmd).then(() => {
    const btn = document.getElementById('exec-copy-btn');
    btn.textContent = '✓';
    setTimeout(() => btn.textContent = '⧉', 1500);
  });
}

function hideExecDialog(e) {
  if (e && e.target !== document.getElementById('exec-overlay')) return;
  document.getElementById('exec-overlay').classList.remove('show');
}

async function execClaude() {
  const skip = document.getElementById('exec-skip-perms').checked;
  const terminal = document.getElementById('exec-terminal').value;
  hideExecDialog();
  try {
    await inv('exec_claude', { project_id: currentProject, session_id: currentSession || '', skip_permissions: skip, terminal: terminal });
    setStatus('Launched Claude Code in ' + terminal + '.');
  } catch(e) {
    setStatus('Exec failed: ' + e);
  }
}

let pendingIdealizeUuids = [];

async function startIdealize() {
  if (deletedUuids.size === 0) return;
  pendingIdealizeUuids = [...deletedUuids];
  document.getElementById('idealize-desc').textContent =
    `${pendingIdealizeUuids.length} messages selected. Generating idealized conversation...`;
  document.getElementById('idealize-ta').value = '';
  document.getElementById('idealize-preview').innerHTML = '';
  document.getElementById('idealize-apply-btn').disabled = true;
  document.getElementById('idealize-overlay').classList.add('show');
  setStatus('Idealizing with Claude...');

  // Stream tokens into textarea as they arrive
  if (window.runtime) {
    window.runtime.EventsOn('claude:stream', (line) => {
      try {
        const ev = JSON.parse(line);
        const msg = ev?.message?.content;
        if (Array.isArray(msg)) {
          msg.forEach(b => {
            if (b.type === 'text' && b.text) {
              document.getElementById('idealize-ta').value += b.text;
            }
          });
        }
      } catch(_) {}
    });
  }

  try {
    const raw = await inv('idealize_messages', {
      project_id: currentProject,
      session_id: currentSession,
      uuids: pendingIdealizeUuids,
    });
    if (window.runtime) window.runtime.EventsOff('claude:stream');
    // Handle possible double-JSON-encoding
    let parsed = JSON.parse(raw);
    if (typeof parsed === 'string') parsed = JSON.parse(parsed);
    document.getElementById('idealize-ta').value = JSON.stringify(parsed, null, 2);
    console.log('idealize result:', parsed);
    renderIdealizePreview(parsed);
    const mode = parsed.mode || 'actions';
    const items = mode === 'rewrite' ? (parsed.messages || []) : (parsed.actions || []);
    const count = items.length;
    document.getElementById('idealize-desc').textContent =
      mode === 'rewrite'
        ? `Rewrite mode: ${count} messages generated. Review and apply.`
        : `Actions mode: ${count} per-message actions. Review and apply.`;
    document.getElementById('idealize-apply-btn').disabled = false;
    setStatus('Idealization ready.');
  } catch(e) {
    if (window.runtime) window.runtime.EventsOff('claude:stream');
    document.getElementById('idealize-desc').textContent = 'Error: ' + e;
    setStatus('Idealize failed: ' + e);
  }
}

function renderIdealizePreview(parsed) {
  const container = document.getElementById('idealize-preview');
  const mode = parsed.mode || 'actions';

  if (mode === 'rewrite') {
    // Rewrite mode: show new messages as chat bubbles
    container.innerHTML = (parsed.messages || []).map(m => {
      const roleClass = m.role === 'user' ? 'role-user' : 'role-assistant';
      let contentHtml = '';
      if (Array.isArray(m.content)) {
        contentHtml = m.content.map(c => renderContentBlock(c)).join('');
      } else if (typeof m.content === 'string') {
        contentHtml = `<div class="msg-text">${escHtml(m.content.slice(0, 2000))}</div>`;
      }
      return `<div style="margin-bottom:6px"><div class="msg-card ${roleClass}" style="border-radius:6px;overflow:hidden"><div class="msg-header"><span class="role-badge">${m.role}</span></div><div class="msg-content">${contentHtml || '<span style="color:#484f58">—</span>'}</div></div></div>`;
    }).join('');
  } else {
    // Actions mode: show per-message actions with color coding
    const msgMap = {};
    allMessages.forEach(m => { msgMap[m.uuid] = m; });
    container.innerHTML = (parsed.actions || []).map(a => {
      const orig = msgMap[a.uuid];
      const role = orig ? orig.role : '?';
      const roleClass = role === 'user' ? 'role-user' : 'role-assistant';
      const actionColors = { delete: '#f85149', keep: '#3fb950', edit: '#d29922' };
      const actionColor = actionColors[a.action] || '#8b949e';
      // Extract text from original message raw content
      let preview = '';
      if (orig?.raw?.message?.content) {
        const content = orig.raw.message.content;
        if (Array.isArray(content)) {
          preview = content.map(c => {
            if (c.type === 'text') return c.text || '';
            if (c.type === 'tool_use') return `[tool: ${c.name}]`;
            if (c.type === 'tool_result') return typeof c.content === 'string' ? c.content : '[tool result]';
            return '';
          }).filter(Boolean).join('\n').slice(0, 500);
        } else if (typeof content === 'string') {
          preview = content.slice(0, 500);
        }
      }
      if (!preview && orig?.content_summary?.text_preview) {
        preview = orig.content_summary.text_preview.slice(0, 500);
      }
      let editedHtml = '';
      if (a.action === 'edit' && a.edited_content) {
        editedHtml = `<div style="margin-top:4px;padding:6px 8px;background:#1c2128;border-left:2px solid ${actionColor};font-size:12px;color:#e6edf3"><strong style="color:${actionColor}">Edited:</strong> ${escHtml(a.edited_content.slice(0, 500))}</div>`;
      }
      return `<div style="margin-bottom:4px;opacity:${a.action === 'delete' ? 0.5 : 1}">
        <div class="msg-card ${roleClass}" style="border-radius:6px;overflow:hidden;border-left:3px solid ${actionColor}">
          <div class="msg-header"><span class="role-badge">${role}</span><span style="margin-left:auto;font-size:11px;color:${actionColor};font-weight:600">${a.action.toUpperCase()}</span></div>
          <div class="msg-content"><div class="msg-text" style="font-size:12px;color:#8b949e;white-space:pre-wrap">${escHtml(preview) || '—'}</div>${editedHtml}</div>
        </div></div>`;
    }).join('');
  }
}

function hideIdealizeDialog(e) {
  if (e && e.target !== document.getElementById('idealize-overlay')) return;
  document.getElementById('idealize-overlay').classList.remove('show');
}

function generateUUID() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
    const r = Math.random() * 16 | 0;
    return (c === 'x' ? r : (r & 0x3 | 0x8)).toString(16);
  });
}

async function applyIdealized() {
  const jsonStr = document.getElementById('idealize-ta').value.trim();
  if (!jsonStr) return;
  let parsed;
  try { parsed = JSON.parse(jsonStr); } catch(e) { setStatus('Invalid JSON: ' + e); return; }
  pushUndo();
  hideIdealizeDialog();

  const mode = parsed.mode || 'actions';

  if (mode === 'rewrite') {
    // Rewrite mode: replace selected messages with new ones
    const firstSelIdx = allMessages.findIndex(m => pendingIdealizeUuids.includes(m.uuid));
    const firstParentUuid = firstSelIdx > 0 ? allMessages[firstSelIdx - 1]?.uuid : null;

    const newLines = [];
    const newMsgObjects = [];
    let prevUuid = firstParentUuid;
    for (const im of parsed.messages) {
      const uuid = generateUUID();
      const content = typeof im.content === 'string' ? [{ type: 'text', text: im.content }] : im.content;
      const rawLine = JSON.stringify({
        uuid,
        parentUuid: prevUuid,
        type: im.role,
        timestamp: new Date().toISOString(),
        message: { role: im.role, content },
        sessionId: currentSession,
        isSidechain: false,
      });
      newLines.push(rawLine);
      newMsgObjects.push({
        uuid,
        parentUuid: prevUuid,
        role: im.role,
        type: im.role,
        timestamp: new Date().toISOString(),
        isSidechain: false,
        is_tool_only: false,
        content_summary: { types: ['text'], text_preview: typeof im.content === 'string' ? im.content.slice(0, 100) : '', size: JSON.stringify(content).length },
        raw: JSON.parse(rawLine),
      });
      prevUuid = uuid;
    }

    pendingInsertLines = newLines;
    pendingDeletedUuids = [...pendingIdealizeUuids];

    const selSet = new Set(pendingIdealizeUuids);
    const firstIdx = allMessages.findIndex(m => selSet.has(m.uuid));
    allMessages = allMessages.filter(m => !selSet.has(m.uuid));
    allMessages.splice(firstIdx, 0, ...newMsgObjects);
    pendingIdealizeUuids.forEach(u => deletedUuids.add(u));

    setStatus(`Applied rewrite (${parsed.messages.length} messages). Click Save to write.`);
  } else {
    // Actions mode: apply per-message delete/keep/edit
    const toDelete = [];
    const toKeep = [];
    const editLines = [];
    for (const a of parsed.actions) {
      if (a.action === 'delete') {
        toDelete.push(a.uuid);
      } else if (a.action === 'keep') {
        toKeep.push(a.uuid);
      } else if (a.action === 'edit' && a.edited_content) {
        toKeep.push(a.uuid); // edited messages are kept
        const msg = allMessages.find(m => m.uuid === a.uuid);
        if (msg) {
          const newContent = [{ type: 'text', text: a.edited_content }];
          msg.raw.message = { role: msg.role, content: newContent };
          msg.content_summary = { types: ['text'], text_preview: a.edited_content.slice(0, 100), size: a.edited_content.length };
          const newRaw = { ...msg.raw };
          newRaw.message = { role: msg.role, content: newContent };
          editLines.push(JSON.stringify(newRaw));
        }
      }
    }

    // Keep/edit: remove from deletedUuids (unselect)
    toKeep.forEach(u => deletedUuids.delete(u));
    // Delete: ensure in deletedUuids
    toDelete.forEach(u => deletedUuids.add(u));
    pendingDeletedUuids = [...new Set([...(pendingDeletedUuids || []), ...toDelete])];
    if (editLines.length > 0) {
      pendingInsertLines = [...(pendingInsertLines || []), ...editLines];
    }

    const deleteCount = toDelete.length;
    const editCount = parsed.actions.filter(a => a.action === 'edit').length;
    const keepCount = parsed.actions.filter(a => a.action === 'keep').length;
    setStatus(`Actions applied: ${deleteCount} deleted, ${editCount} edited, ${keepCount} kept. Click Save to write.`);
  }

  renderConversation();
  updateButtons();
}

let pendingSummaryUuids = [];

async function startSummarize() {
  if (deletedUuids.size === 0) return;
  pendingSummaryUuids = [...deletedUuids];
  document.getElementById('summarize-desc').textContent =
    `${pendingSummaryUuids.length} messages selected. Generating summary with Claude...`;
  document.getElementById('summarize-ta').value = '';
  document.getElementById('summarize-apply-btn').disabled = true;
  document.getElementById('summarize-overlay').classList.add('show');
  setStatus('Summarizing with Claude...');
  try {
    const summary = await inv('summarize_messages', {
      project_id: currentProject,
      session_id: currentSession,
      uuids: pendingSummaryUuids,
    });
    document.getElementById('summarize-ta').value = summary;
    document.getElementById('summarize-desc').textContent =
      `${pendingSummaryUuids.length} messages will be replaced with this summary. You can edit before applying.`;
    document.getElementById('summarize-apply-btn').disabled = false;
    setStatus('Summary ready.');
  } catch(e) {
    document.getElementById('summarize-desc').textContent = 'Error: ' + e;
    setStatus('Summarize failed: ' + e);
  }
}

function hideSummarizeDialog(e) {
  if (e && e.target !== document.getElementById('summarize-overlay')) return;
  document.getElementById('summarize-overlay').classList.remove('show');
}

// --- Text-to-Image ---
async function startT2i() {
  if (!currentProject || !currentSession) return;
  const desc = document.getElementById('t2i-desc');
  const iframe = document.getElementById('t2i-iframe');
  const applyBtn = document.getElementById('t2i-apply-btn');
  desc.textContent = 'Generating preview...';
  iframe.srcdoc = '<html><body style="background:#0d1117;color:#8b949e;font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:100vh"><p>Rendering conversation as images...</p></body></html>';
  applyBtn.disabled = true;
  document.getElementById('t2i-overlay').classList.add('show');

  try {
    const result = await go.CompactToImagePreview(currentProject, currentSession);
    iframe.srcdoc = result.html;
    const saved = result.report.total_saved;
    const pct = (saved * 100 / result.report.total_before).toFixed(1);
    desc.textContent = `${humanSize(result.report.total_before)} → ${humanSize(result.report.total_after)} (${pct}% bytes saved). Token savings measured via intercept proxy.`;
    applyBtn.disabled = false;
  } catch (e) {
    desc.textContent = 'Error: ' + e;
  }
}

function hideT2iDialog(e) {
  if (e && e.target !== document.getElementById('t2i-overlay')) return;
  document.getElementById('t2i-overlay').classList.remove('show');
}

async function applyT2i() {
  if (!currentProject || !currentSession) return;
  const desc = document.getElementById('t2i-desc');
  const applyBtn = document.getElementById('t2i-apply-btn');
  applyBtn.disabled = true;
  desc.textContent = 'Applying (rendering images via weasyprint)...';

  try {
    const result = await go.CompactToImageApply(currentProject, currentSession);
    desc.textContent = 'Done! New session: ' + result.new_session;
    await loadSessions(currentProject);
  } catch (e) {
    desc.textContent = 'Error: ' + e;
  }
}

function humanSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024*1024) return (bytes/1024).toFixed(1) + ' KB';
  return (bytes/1024/1024).toFixed(1) + ' MB';
}

async function applySummary() {
  const summary = document.getElementById('summarize-ta').value.trim();
  if (!summary) return;
  pushUndo();
  hideSummarizeDialog();
  setStatus('Applying summary...');
  try {
    await inv('apply_summary', {
      project_id: currentProject,
      session_id: currentSession,
      uuids: pendingSummaryUuids,
      summary,
    });
    deletedUuids = new Set();
    setStatus('Summary applied. Reloading...');
    await loadConversation(currentSession);
  } catch(e) {
    setStatus('Apply failed: ' + e);
  }
}

async function branchFrom(index) {
  const msg = allMessages[index];
  if (!msg) return;
  setStatus('Branching...');
  try {
    const newSessionId = await inv('branch_new_session', {
      project_id: currentProject,
      session_id: currentSession,
      uuid: msg.uuid,
    });
    setStatus('Branched to new session.');
    // reload sessions and navigate to new one
    await loadSessions(currentProject);
    const el = document.querySelector(`#session-list [data-id="${newSessionId}"]`);
    if (el) { el.classList.add('active'); await selectSession(newSessionId, el); }
  } catch(e) {
    setStatus('Branch failed: ' + e);
  }
}

async function restoreSidechain(index) {
  const msg = allMessages[index];
  if (!msg) return;
  setStatus('Restoring sidechain...');
  try {
    await inv('restore_sidechain', { project_id: currentProject, session_id: currentSession, uuid: msg.uuid });
    setStatus('Restored. Reloading...');
    await loadConversation(currentSession);
  } catch(e) {
    setStatus('Restore failed: ' + e);
  }
}

function computeWasteScore(msg) {
  // Returns score: -1 (productive) → 0 (neutral) → +1 (wasteful)
  if (msg.is_compact_boundary) {
    return { score: 0, label: 'compaction', size: 0, isCompaction: true };
  }
  const cs = msg.content_summary;
  const types = cs?.types || [];
  const raw = msg.raw?.message?.content;
  const size = cs?.size || 0;

  const progressTools = ['Write', 'Edit', 'NotebookEdit', 'write', 'edit'];

  let score = 0;
  let label = 'text';

  if (Array.isArray(raw)) {
    let hasError = false, hasProgress = false;
    for (const b of raw) {
      if (b.type === 'tool_result' && b.is_error) hasError = true;
      if (b.type === 'tool_use' && progressTools.some(t => (b.name || '').includes(t))) hasProgress = true;
    }
    if (hasError) { score = 1.0; label = 'error'; }
    else if (hasProgress) { score = -1.0; label = 'write'; }
    else {
      const isToolOnly = types.length > 0 && types.every(t => t === 'tool_use' || t === 'tool_result');
      if (isToolOnly) { score = 0.4; label = 'tool'; }
    }
  }

  if (label === 'text' && types.includes('thinking')) { score = 0.1; label = 'thinking'; }

  // Large tool-only messages = more wasteful
  if (score > 0 && size > 5000) score = Math.min(1, score + 0.2);

  return { score, label, size };
}

function wasteColor(score) {
  // score: -1 (productive/blue) → 0 (neutral/gray) → 1 (wasteful/red)
  const r = score > 0 ? Math.round(31 + 217 * score) : Math.round(31 - 0 * score);
  const g = score > 0 ? Math.round(81 - 30 * score) : Math.round(81 + 30 * (-score));
  const b = score > 0 ? Math.round(88 - 15 * score) : Math.round(88 + 147 * (-score));
  return `rgb(${Math.min(255,r)},${Math.min(255,g)},${Math.min(255,b)})`;
}

let hmDragStart = null;
let hmDragEnd = null;
let hmDragging = false;

function renderHeatmap() {
  const container = document.getElementById('conv-heatmap');
  if (!allMessages.length) { container.style.display = 'none'; return; }
  container.style.display = 'flex';

  container.innerHTML = allMessages.map((msg, i) => {
    const w = computeWasteScore(msg);
    if (w.isCompaction) {
      return `<div class="conv-heatmap-cell" data-idx="${i}" style="background:#d29922;min-width:3px;max-width:3px"><span class="tooltip">⚡ Compaction</span></div>`;
    }
    const color = wasteColor(w.score);
    const deleted = deletedUuids.has(msg.uuid);
    const opacity = deleted ? 0.2 : 1;
    return `<div class="conv-heatmap-cell" data-idx="${i}" style="background:${color};opacity:${opacity}"><span class="tooltip">#${i+1} ${msg.role} · ${w.label} · ${formatSize(w.size)}</span></div>`;
  }).join('');

  // Drag selection handlers
  container.onmousedown = (e) => {
    const cell = e.target.closest('.conv-heatmap-cell');
    if (!cell) return;
    hmDragStart = parseInt(cell.dataset.idx);
    hmDragEnd = hmDragStart;
    hmDragging = true;
    updateHeatmapRange();
    e.preventDefault();
  };
  container.onmousemove = (e) => {
    if (!hmDragging) return;
    const cell = e.target.closest('.conv-heatmap-cell');
    if (!cell) return;
    hmDragEnd = parseInt(cell.dataset.idx);
    updateHeatmapRange();
  };
  container.onmouseup = (e) => {
    if (!hmDragging) return;
    hmDragging = false;
    const lo = Math.min(hmDragStart, hmDragEnd);
    const hi = Math.max(hmDragStart, hmDragEnd);
    if (lo === hi) {
      // Single click = scroll to message
      scrollToMsg(lo);
      clearHeatmapRange();
      return;
    }
    // Range select: toggle selection for range
    pushUndo();
    const rangeUuids = allMessages.slice(lo, hi + 1).map(m => m.uuid);
    const allSelected = rangeUuids.every(u => deletedUuids.has(u));
    for (let i = lo; i <= hi; i++) {
      setMessageChecked(i, !allSelected);
    }
    clearHeatmapRange();
    updateSizeInfo();
    updateButtons();
    renderHeatmap();
  };
  container.onmouseleave = () => {
    if (hmDragging) {
      hmDragging = false;
      clearHeatmapRange();
    }
  };
}

function updateHeatmapRange() {
  const lo = Math.min(hmDragStart, hmDragEnd);
  const hi = Math.max(hmDragStart, hmDragEnd);
  document.querySelectorAll('.conv-heatmap-cell').forEach(cell => {
    const idx = parseInt(cell.dataset.idx);
    cell.classList.toggle('hm-range', idx >= lo && idx <= hi);
  });
}

function clearHeatmapRange() {
  document.querySelectorAll('.conv-heatmap-cell.hm-range').forEach(c => c.classList.remove('hm-range'));
}

function scrollToMsg(index) {
  const el = document.getElementById(`msg-${index}`);
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'center' });
}

function updateSizeInfo(totalSize) {
  const el = document.getElementById('size-info');
  const deletedSize = allMessages
    .filter(m => deletedUuids.has(m.uuid))
    .reduce((s, m) => s + (m.content_summary?.size || 0), 0);
  const totalEstimate = allMessages.reduce((s, m) => s + (m.content_summary?.size || 0), 0);

  if (deletedUuids.size > 0) {
    el.className = 'size-info has-selection';
    el.textContent = `${deletedUuids.size} selected to delete (−${formatSize(deletedSize)} / ${formatSize(totalEstimate)})`;
  } else {
    el.className = 'size-info';
    const ts = totalSize !== undefined ? totalSize : totalEstimate;
    el.textContent = `${allMessages.length} messages · ${formatSize(ts)}`;
  }
}

function updateButtons() {
  const hasDeleted = deletedUuids.size > 0;
  document.getElementById('delete-btn').disabled = !hasDeleted;
  document.getElementById('save-btn').disabled = !hasDeleted;
  document.getElementById('summarize-btn').disabled = !hasDeleted;
  document.getElementById('idealize-btn').disabled = !hasDeleted;
}

function deleteSelected() {
  pushUndo();
  // Visually hide deleted messages (not saved yet)
  deletedUuids.forEach(uuid => {
    const el = document.querySelector(`[data-uuid="${uuid}"]`);
    if (el) el.classList.add('hidden');
  });
  updateSizeInfo();
  setStatus(`${deletedUuids.size} messages marked for deletion. Click Save to write.`);
}

// Save dialog
function showSaveDialog() {
  const keepCount = allMessages.filter(m => !deletedUuids.has(m.uuid)).length;
  const deleteCount = deletedUuids.size;
  const deletedSize = allMessages
    .filter(m => deletedUuids.has(m.uuid))
    .reduce((s, m) => s + (m.content_summary?.size || 0), 0);
  const totalEstimate = allMessages.reduce((s, m) => s + (m.content_summary?.size || 0), 0);

  document.getElementById('save-stat').innerHTML = `
    <div class="stat-row"><span class="label">Total messages</span><span class="value">${allMessages.length}</span></div>
    <div class="stat-row"><span class="label">Messages to delete</span><span class="value red">−${deleteCount}</span></div>
    <div class="stat-row"><span class="label">Messages to keep</span><span class="value green">${keepCount}</span></div>
    <div class="stat-row"><span class="label">Estimated size reduction</span><span class="value red">−${formatSize(deletedSize)}</span></div>
  `;
  document.getElementById('save-overlay').classList.add('show');
}

function hideSaveDialog(e) {
  if (e && e.target !== document.getElementById('save-overlay')) return;
  document.getElementById('save-overlay').classList.remove('show');
}

async function executeSave() {
  hideSaveDialog();
  const keepUuids = allMessages
    .filter(m => !deletedUuids.has(m.uuid))
    .map(m => m.uuid)
    .filter(Boolean);

  setStatus('Saving...');
  try {
    const data = await inv('save_conversation', {
      project_id: currentProject,
      session_id: currentSession,
      req: {
        keep_uuids: keepUuids,
        deleted_uuids: pendingDeletedUuids,
        insert_lines: pendingInsertLines,
      },
    });
    setStatus(`Saved! ${formatSize(data.new_size)} · backup: ${data.backup}`);
    // Reload
    await loadConversation(currentSession);
    await loadSessions(currentProject);
  } catch (e) {
    setStatus('Error: ' + e.message);
  }
}

// Init: handle #project=X&session=Y from surgery.sh
async function initFromHash() {
  const hash = location.hash.slice(1);
  if (!hash) { await loadProjects(); return; }

  const params = Object.fromEntries(hash.split('&').map(p => p.split('=')));
  await loadProjects();

  if (params.project) {
    const el = document.querySelector(`#project-list [data-id="${params.project}"]`);
    if (el) {
      el.classList.add('active');
      currentProject = params.project;
      await loadSessions(params.project);

      if (params.session) {
        const sel = document.querySelector(`#session-list [data-id="${params.session}"]`);
        if (sel) {
          sel.classList.add('active');
          await selectSession(params.session, sel);
        }
      }
    }
  }
}


function reloadConversation() { loadConversation(currentSession); }
function setCurrentProject(v) { currentProject = v; }
function setCurrentSession(v) { currentSession = v; }

async function checkUpdate() {
  try {
    const info = await go.CheckUpdate();
    document.getElementById('version-badge').textContent = `v${info.current_version}`;
    if (info.has_update && info.download_url) {
      const badge = document.getElementById('update-badge');
      badge.innerHTML = `<span class="update-badge" onclick="doUpdate('${info.download_url}')">↑ v${info.latest_version} available</span>`;
    }
  } catch(e) {}
}

async function doUpdate(url) {
  setStatus('Downloading update...');
  try {
    await go.DoUpdate(url);
  } catch(e) {
    setStatus('Update failed: ' + e);
  }
}

// Expose functions to window for HTML onclick handlers
Object.assign(window, {
  loadProjects, loadSessions, setCurrentProject, setCurrentSession,
  selectAll, toggleTools, toggleSidechain, loadConversation, reloadConversation,
  showExecDialog, hideExecDialog, copyExecCommand, execClaude,
  startIdealize, hideIdealizeDialog, applyIdealized,
  startSummarize, hideSummarizeDialog, applySummary,
  startT2i, hideT2iDialog, applyT2i,
  showSaveDialog, hideSaveDialog, executeSave,
  hideInsertDialog, commitInsert,
  deleteSelected, handleCheckboxClick, toggleContent,
  selectProject, selectSession, commitEdit, cancelEdit,
  checkUpdate, doUpdate, initFromHash,
});
