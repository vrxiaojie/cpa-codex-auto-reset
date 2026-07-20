'use strict';

const apiBase = '/v0/management/plugins/cpa-codex-auto-reset';
const pageSize = 15;
const state = { accounts: [], logs: [], page: 1, selected: new Set() };
const authStorageKey = 'cli-proxy-auth';
const legacyManagementKeyStorageKey = 'managementKey';
const storagePrefix = 'enc::v1::';
const storageSalt = 'cli-proxy-api-webui::secure-storage';

const $ = (id) => document.getElementById(id);
const formatTime = (value) => {
  if (!value) return '—';
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) || parsed.getUTCFullYear() <= 1
    ? '—'
    : new Intl.DateTimeFormat('zh-CN', {dateStyle:'short',timeStyle:'medium'}).format(parsed);
};

function decodeStorageValue(value) {
  if (!value || !value.startsWith(storagePrefix)) return value;
  const encrypted = atob(value.slice(storagePrefix.length));
  const data = Uint8Array.from(encrypted, (character) => character.charCodeAt(0));
  const key = new TextEncoder().encode(`${storageSalt}|${window.location.host}|${navigator.userAgent}`);
  const decoded = new Uint8Array(data.length);
  data.forEach((byte, index) => { decoded[index] = byte ^ key[index % key.length]; });
  return new TextDecoder().decode(decoded);
}

function readStoredJSON(storage, key) {
  try {
    const raw = storage.getItem(key);
    if (!raw) return null;
    const decoded = decodeStorageValue(raw);
    try { return JSON.parse(decoded); } catch { return decoded; }
  } catch { return null; }
}

function readManagementKey() {
  const persisted = readStoredJSON(window.localStorage, authStorageKey);
  const current = persisted && persisted.state ? persisted.state.managementKey : persisted && persisted.managementKey;
  if (typeof current === 'string' && current.trim()) return current.trim();
  const legacy = readStoredJSON(window.localStorage, legacyManagementKeyStorageKey);
  return typeof legacy === 'string' ? legacy.trim() : '';
}

async function request(path, options = {}) {
  const managementKey = readManagementKey();
  if (!managementKey) {
    const error = new Error('未找到 CPA 管理页的登录凭据，请返回管理页重新连接并选择记住密钥。');
    error.status = 401;
    throw error;
  }
  const response = await fetch(apiBase + path, {
    credentials: 'same-origin',
    ...options,
    headers: {'Content-Type':'application/json', 'Authorization':`Bearer ${managementKey}`, ...(options.headers || {})}
  });
  const payload = await response.json().catch(() => ({error:'invalid_response'}));
  if (!response.ok) {
    const error = new Error(payload.error || `HTTP ${response.status}`);
    error.status = response.status;
    throw error;
  }
  return payload;
}

function showError(message) {
  const banner = $('errorBanner');
  banner.textContent = message || '';
  banner.classList.toggle('hidden', !message);
}

function textCell(row, value, className) {
  const cell = document.createElement('td');
  const node = document.createElement('div');
  node.textContent = value;
  if (className) node.className = className;
  cell.appendChild(node);
  row.appendChild(cell);
  return cell;
}

function filteredAccounts() {
  const query = $('searchInput').value.trim().toLowerCase();
  const filter = $('participationFilter').value;
  return state.accounts.filter((item) => {
    const matchesText = !query || [item.label,item.email,item.file_name,item.id].some((value) => (value || '').toLowerCase().includes(query));
    const matchesParticipation = filter === 'all' || (filter === 'participating' && item.participating) || (filter === 'not-participating' && !item.participating);
    return matchesText && matchesParticipation;
  });
}

function renderAccounts() {
  const all = filteredAccounts();
  const pages = Math.max(1, Math.ceil(all.length / pageSize));
  state.page = Math.min(state.page, pages);
  const visible = all.slice((state.page - 1) * pageSize, state.page * pageSize);
  const body = $('accountRows');
  body.replaceChildren();
  visible.forEach((item) => {
    const row = document.createElement('tr');
    const selectCell = document.createElement('td');
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox'; checkbox.checked = state.selected.has(item.id); checkbox.setAttribute('aria-label', `选择 ${item.label || item.id}`);
    checkbox.addEventListener('change', () => checkbox.checked ? state.selected.add(item.id) : state.selected.delete(item.id));
    selectCell.appendChild(checkbox); row.appendChild(selectCell);

    const accountCell = textCell(row, item.label || '未命名账号', 'account-name');
    const detail = document.createElement('div'); detail.className = 'account-detail'; detail.textContent = item.email || item.file_name || item.id; accountCell.appendChild(detail);

    const participationCell = document.createElement('td');
    const toggle = document.createElement('button'); toggle.type = 'button'; toggle.className = `badge ${item.participating ? 'good' : ''}`; toggle.textContent = item.participating ? '已参与' : '未参与';
    toggle.addEventListener('click', () => updateParticipation([item.id], !item.participating)); participationCell.appendChild(toggle); row.appendChild(participationCell);

    textCell(row, item.available_credits ? `${item.available_credits} 个 · ${formatTime(item.earliest_expires_at)}` : '无');
    const usage = item.blocked ? '已阻塞' : `${Number(item.used_percent || 0).toFixed(0)}%`; const usageCell = textCell(row, usage); usageCell.firstChild.className = `badge ${item.blocked ? 'bad' : ''}`;
    textCell(row, item.last_result || item.error_code || '—');
    textCell(row, formatTime(item.next_allowed_at));
    body.appendChild(row);
  });
  $('pageInfo').textContent = `第 ${state.page} / ${pages} 页`;
  $('prevPage').disabled = state.page <= 1;
  $('nextPage').disabled = state.page >= pages;
  $('accountSummary').textContent = `共 ${all.length} 个匹配账号，已选择 ${state.selected.size} 个`;
  $('selectAll').checked = visible.length > 0 && visible.every((item) => state.selected.has(item.id));
}

function renderLogs() {
  const list = $('logList'); list.replaceChildren();
  state.logs.slice(0, 100).forEach((item) => {
    const row = document.createElement('article'); row.className = 'log';
    const time = document.createElement('time'); time.textContent = formatTime(item.time); row.appendChild(time);
    const meta = document.createElement('div'); meta.className = 'meta'; meta.textContent = [item.account_ref,item.credit_ref].filter(Boolean).join(' · ') || item.trigger || '系统'; row.appendChild(meta);
    const message = document.createElement('div'); message.textContent = [item.event,item.decision,item.outcome,item.error_code].filter(Boolean).join(' · '); row.appendChild(message);
    list.appendChild(row);
  });
  if (!state.logs.length) { const empty = document.createElement('p'); empty.textContent = '暂无重置日志。'; list.appendChild(empty); }
}

async function load() {
  try {
    const status = await request('/status');
    const [accounts, logs] = await Promise.all([request('/accounts'), request('/logs')]);
    state.accounts = accounts.accounts || []; state.logs = logs.logs || [];
    $('runStatus').textContent = status.config.enabled && status.config.complete ? '运行中' : '配置不完整';
    $('resetThreshold').textContent = `${Number(status.config.reset_threshold)}%`;
    $('lastScan').textContent = formatTime(status.last_scan && status.last_scan.finished_at);
    $('nextScan').textContent = formatTime(status.next_scan);
    $('participatingCount').textContent = `${status.counts.participating} / ${status.counts.total}`;
    $('creditCount').textContent = String(status.counts.with_credits);
    $('cooldownCount').textContent = String(status.counts.cooling_or_backoff);
    const warning = $('warningBanner');
    warning.textContent = status.config.remote_management_warning ? 'Management API 使用远程地址，请确认 TLS 与网络边界安全。' : '';
    warning.classList.toggle('hidden', !status.config.remote_management_warning);
    renderAccounts(); renderLogs(); showError('');
  } catch (error) {
    showError(`读取插件状态失败：${error.message}`);
  }
}

async function updateParticipation(ids, participating) {
  if (!ids.length) return;
  try {
    await request('/accounts/participation', {method:'PUT', body:JSON.stringify({auth_ids:ids,participating})});
    ids.forEach((id) => state.selected.delete(id)); await load();
  } catch (error) { showError(`更新参与状态失败：${error.message}`); }
}

async function scan() {
  const button = $('scanButton'); button.disabled = true; button.textContent = '扫描中…';
  try { await request('/scan', {method:'POST', body:'{}'}); await load(); }
  catch (error) { showError(`扫描未启动：${error.message}`); }
  finally { button.disabled = false; button.textContent = '立即扫描'; }
}

$('searchInput').addEventListener('input', () => { state.page = 1; renderAccounts(); });
$('participationFilter').addEventListener('change', () => { state.page = 1; renderAccounts(); });
$('prevPage').addEventListener('click', () => { state.page -= 1; renderAccounts(); });
$('nextPage').addEventListener('click', () => { state.page += 1; renderAccounts(); });
$('scanButton').addEventListener('click', scan);
$('batchJoin').addEventListener('click', () => updateParticipation([...state.selected], true));
$('batchLeave').addEventListener('click', () => updateParticipation([...state.selected], false));
$('selectAll').addEventListener('change', (event) => {
  const all = filteredAccounts().slice((state.page - 1) * pageSize, state.page * pageSize);
  all.forEach((item) => event.target.checked ? state.selected.add(item.id) : state.selected.delete(item.id)); renderAccounts();
});

document.addEventListener('visibilitychange', () => { if (!document.hidden) load(); });
load();
setInterval(() => { if (!document.hidden) load(); }, 15000);
