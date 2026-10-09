'use strict';
const $ = id => document.getElementById(id);
let csrfToken = null, permissions = {}, statusTimer = null, currentUser = null, changingPassword = false;
let currentPage = 1, lastSearch = null, requestNumber = 0;
try { const saved = localStorage.getItem('theme'); if (['light', 'dark'].includes(saved)) { document.documentElement.dataset.theme = saved; $('theme').value = saved; } } catch {}
$('theme').addEventListener('change', () => {
  const value = $('theme').value;
  if (value === 'auto') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = value;
  try { localStorage.setItem('theme', value); } catch {}
});
async function api(url, options) {
  const headers = new Headers(options?.headers);
  if (options?.method && !['GET', 'HEAD'].includes(options.method.toUpperCase())) headers.set('X-CSRF-Token', csrfToken || '');
  const response = await fetch(url, {...options, headers, credentials: 'same-origin', cache: 'no-store'});
  if (response.status === 401) { if (!changingPassword) window.location.replace('/login'); throw new Error('Bitte erneut anmelden.'); }
  if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || `HTTP ${response.status}`); }
  return response.status === 202 ? null : response.json();
}
function element(tag, text, className) { const node = document.createElement(tag); node.textContent = text; if (className) node.className = className; return node; }
function highlightedExcerpt(hit) {
  const excerpt = element('p', '', 'excerpt');
  if (!Array.isArray(hit.snippet_parts)) { excerpt.textContent = hit.snippet; return excerpt; }
  for (const part of hit.snippet_parts) {
    excerpt.append(part.match ? element('mark', part.text) : document.createTextNode(part.text));
  }
  return excerpt;
}
async function search(page) {
  if (!lastSearch) return;
  const request = ++requestNumber;
  $('message').textContent = 'Suche läuft …'; $('results').setAttribute('aria-busy', 'true');
  try {
    const params = new URLSearchParams({...lastSearch, page, limit: 20});
    const result = await api(`/api/search?${params}`);
    if (request !== requestNumber) return;
    currentPage = result.page;
    $('results').replaceChildren();
    for (const hit of result.hits) {
      const card = element('article', '', 'hit');
      card.append(element('h2', hit.path.split('/').pop()), element('p', hit.path, 'path'), highlightedExcerpt(hit));
      card.append(element('p', `Seite ${hit.page} · Datei geändert: ${new Date(hit.modified).toLocaleDateString('de-DE')}`, 'meta'));
      const link = element('a', 'PDF öffnen'); link.href = `/api/documents/${hit.document_id}/pdf#page=${hit.page}`; link.target = '_blank'; link.rel = 'noopener'; card.append(link);
      const retry = element('button', 'OCR erneut ausführen', 'secondary'); retry.type = 'button'; retry.addEventListener('click', () => reprocess(hit.document_id, true, retry)); if (permissions.manage_imports) card.append(retry);
      $('results').append(card);
    }
    $('message').textContent = result.total === 1 ? '1 passendes Dokument' : result.total ? `${result.total} passende Dokumente` : 'Keine passenden Dokumente gefunden.';
    const pages = Math.max(1, Math.ceil(result.total / result.limit));
    $('pagination').hidden = result.total <= result.limit; $('page-label').textContent = `Seite ${currentPage} von ${pages}`;
    $('previous').disabled = currentPage <= 1; $('next').disabled = currentPage >= pages;
  } catch (error) { if (request === requestNumber) { $('message').textContent = `Suche fehlgeschlagen: ${error.message}`; $('results').replaceChildren(); $('pagination').hidden = true; } }
  finally { if (request === requestNumber) $('results').setAttribute('aria-busy', 'false'); }
}
$('search-form').addEventListener('submit', event => { event.preventDefault(); lastSearch = {q: $('query').value, after: $('after').value, before: $('before').value, sort: $('sort').value}; search(1); });
$('previous').addEventListener('click', () => search(currentPage - 1)); $('next').addEventListener('click', () => search(currentPage + 1));
async function refreshStatus() {
  try {
    const account = await api('/api/auth/me');
    if (currentUser && account.user.role !== currentUser.role) { window.location.reload(); return; }
    const status = await api('/api/status');
    const signature = JSON.stringify([status.documents, status.ready, status.failed, status.queue?.pending, status.queue?.running, status.scan.finished]);
    if (documentView && signature !== archiveSignature) loadDocuments(documentPage);
    archiveSignature = signature;
    $('archive-status').textContent = `${status.ready} durchsuchbar · ${status.failed} fehlgeschlagen`;
    $('scan-status').textContent = status.source_error ? 'Archivverzeichnis nicht verfügbar oder ausgetauscht. Verarbeitung pausiert; der Index bleibt erhalten.' : status.scan.running ? `Abgleich: ${status.scan.current || 'Verzeichnis wird geprüft …'}` : status.scan.error ? `Abgleich fehlgeschlagen: ${status.scan.error}` : status.scan.finished ? `Letzter Abgleich: ${new Date(status.scan.finished).toLocaleString('de-DE')}` : 'Noch kein Abgleich abgeschlossen.';
    const queue = status.queue || {pending: 0, running: 0};
    $('queue-status').textContent = `${queue.pending} warten · ${queue.running} in Verarbeitung`;
    $('processing-status').textContent = queue.active ? `${queue.active.path} · ${queue.active.completed_pages} von ${queue.active.total_pages || '?'} Seiten fertig` : '';
    $('processing-progress').hidden = !queue.active || !queue.active.total_pages;
    if (queue.active) { $('processing-progress').max = Math.max(1, queue.active.total_pages); $('processing-progress').value = queue.active.completed_pages; }
    $('scan').disabled = $('verify').disabled = status.scan.running;
    $('failures-panel').hidden = !status.failures.length; $('failures').replaceChildren();
    for (const failure of status.failures) {
      const row = element('li', `${failure.path}: ${failure.error}`);
      const retry = element('button', 'Erneut versuchen', 'secondary'); retry.type = 'button'; retry.addEventListener('click', () => reprocess(failure.id, false, retry)); if (permissions.manage_imports) row.append(retry);
      const ocr = element('button', 'OCR erzwingen', 'secondary'); ocr.type = 'button'; ocr.addEventListener('click', () => reprocess(failure.id, true, ocr)); if (permissions.manage_imports) row.append(ocr);
      $('failures').append(row);
    }
  } catch { $('archive-status').textContent = 'Verbindung zur Anwendung nicht verfügbar.'; }
}
async function reprocess(id, force, button) {
  button.disabled = true;
  try { await api(`/api/documents/${id}/retry?ocr=${force}`, {method: 'POST'}); await refreshStatus(); if (documentView) await loadDocuments(documentPage); }
  catch (error) { $(documentView ? 'documents-message' : 'message').textContent = `Verarbeitung konnte nicht starten: ${error.message}`; }
  finally { button.disabled = false; }
}
async function triggerScan(full) {
  $('scan').disabled = $('verify').disabled = true;
  try { await api(`/api/scan?full=${full}`, {method: 'POST'}); await refreshStatus(); } catch (error) { $('scan-status').textContent = `Abgleich konnte nicht starten: ${error.message}`; $('scan').disabled = $('verify').disabled = false; }
}
$('scan').addEventListener('click', () => triggerScan(false)); $('verify').addEventListener('click', () => triggerScan(true));


let documentPage = 1, documentRequest = 0, documentFilters = {path: '', status: '', sort: 'path'}, documentView = false, archiveSignature = '';
const documentStatuses = {ready: 'Verarbeitet', queued: 'Wartet', processing: 'In Verarbeitung', error: 'Fehlgeschlagen'};
function switchView(view) {
  const documents = view === 'documents', settings = view === 'settings';
  documentView = documents;
  $('search-view').hidden = documents || settings; $('documents-view').hidden = !documents; $('settings-view').hidden = !settings;
  $('show-search').setAttribute('aria-pressed', String(!documents && !settings)); $('show-documents').setAttribute('aria-pressed', String(documents));
  $('show-settings').setAttribute('aria-pressed', String(settings));
  if (settings) {
    if (permissions.manage_settings) loadSettings();
    if (permissions.manage_users) loadUsers();
  }
  if (documents) loadDocuments(documentPage);
}
$('show-search').addEventListener('click', () => switchView('search'));
$('show-documents').addEventListener('click', () => switchView('documents'));
$('show-settings').addEventListener('click', () => switchView('settings'));
function filterDocuments() {
  documentFilters = {path: $('document-path').value.trim(), status: $('document-status').value, sort: $('document-sort').value};
  loadDocuments(1);
}
$('documents-form').addEventListener('submit', event => {event.preventDefault(); filterDocuments();});
$('document-status').addEventListener('change', filterDocuments); $('document-sort').addEventListener('change', filterDocuments);
$('documents-previous').addEventListener('click', () => loadDocuments(documentPage - 1, true));
$('documents-next').addEventListener('click', () => loadDocuments(documentPage + 1, true));
async function loadDocuments(page, navigate = false) {
  const request = ++documentRequest;
  $('documents-results').setAttribute('aria-busy', 'true');
  $('documents-message').textContent = 'Dokumente werden geladen …';
  try {
    const result = await api(`/api/documents?${new URLSearchParams({...documentFilters, page, limit: 20})}`);
    if (request !== documentRequest) return;
    const pages = Math.max(1, Math.ceil(result.total / result.limit));
    if (result.page > pages) { loadDocuments(pages, navigate); return; }
    documentPage = result.page;
    $('documents-results').replaceChildren();
    for (const doc of result.documents) {
      const card = element('article', '', 'hit');
      const heading = element('div', '', 'document-heading');
      heading.append(element('h2', doc.path.split('/').pop()), element('span', documentStatuses[doc.status] || doc.status, 'document-badge'));
      card.append(heading, element('p', doc.path, 'path'));
      const sizeUnit = doc.size < 1048576 ? 'KiB' : 'MiB';
      const metadata = [`${new Intl.NumberFormat('de-DE', {maximumFractionDigits: 1}).format(doc.size / (sizeUnit === 'KiB' ? 1024 : 1048576))} ${sizeUnit}`, `Geändert: ${new Date(doc.modified).toLocaleDateString('de-DE')}`];
      if (doc.status === 'ready') metadata.unshift(`${doc.pages} ${doc.pages === 1 ? 'Seite' : 'Seiten'}`, `${doc.ocr_pages} mit OCR`);
      card.append(element('p', metadata.join(' · '), 'meta'));
      if (doc.status === 'ready' && doc.text_pages === 0) card.append(element('p', 'Kein lesbarer Text erkannt. Du kannst das PDF öffnen oder die Texterkennung erneut ausführen.', 'hint'));
      if (doc.error) card.append(element('p', doc.error, 'document-error'));
      const actions = element('div', '', 'document-actions');
      const link = element('a', 'PDF öffnen'); link.href = `/api/documents/${doc.id}/pdf`; link.target = '_blank'; link.rel = 'noopener'; actions.append(link);
      if (doc.status === 'ready') {
        const text = element('button', 'Gespeicherten Text ansehen', 'secondary'); text.type = 'button'; text.addEventListener('click', () => openDocumentText(doc, text)); actions.append(text);
      }
      if (permissions.manage_imports && doc.status === 'error') {
        const retry = element('button', 'Erneut versuchen', 'secondary'); retry.type = 'button'; retry.addEventListener('click', () => reprocess(doc.id, false, retry)); actions.append(retry);
      }
      if (permissions.manage_imports && (doc.status === 'ready' || doc.status === 'error')) {
        const ocr = element('button', 'OCR erneut ausführen', 'secondary'); ocr.type = 'button'; ocr.addEventListener('click', () => reprocess(doc.id, true, ocr)); actions.append(ocr);
      }
      card.append(actions); $('documents-results').append(card);
    }
    $('documents-message').textContent = result.total ? `${result.total} ${result.total === 1 ? 'Dokument' : 'Dokumente'}${result.total > result.limit ? ` · ${((documentPage - 1) * result.limit) + 1}–${Math.min(documentPage * result.limit, result.total)} werden angezeigt` : ''}` : 'Keine Dokumente gefunden. Prüfe die Filter oder gleiche Dein Archiv ab.';
    $('documents-pagination').hidden = result.total <= result.limit;
    $('documents-page-label').textContent = `Seite ${documentPage} von ${pages}`;
    $('documents-previous').disabled = documentPage <= 1; $('documents-next').disabled = documentPage >= pages;
    if (navigate) $('documents-title').focus();
  } catch (error) {
    if (request === documentRequest) { $('documents-message').textContent = `Dokumentübersicht konnte nicht geladen werden: ${error.message}`; $('documents-pagination').hidden = true; }
  } finally { if (request === documentRequest) $('documents-results').setAttribute('aria-busy', 'false'); }
}

let textDocument = null, textPage = 1, textRequest = 0, textTrigger = null;
async function openDocumentText(doc, trigger) {
  textTrigger = trigger; textDocument = null; textPage = 1;
  $('document-text-title').textContent = 'Gespeicherter Text'; $('document-text-path').textContent = doc.path;
  $('document-text-content').textContent = ''; $('document-text-empty').hidden = true;
  $('document-text-message').textContent = 'Dokument wird geladen …';
  updateTextNavigation(true);
  $('document-text-dialog').showModal();
  const request = ++textRequest;
  try {
    const current = await api(`/api/documents/${doc.id}`, {cache: 'no-store'});
    if (request !== textRequest) return;
    if (current.status !== 'ready') throw new Error('Das Dokument ist derzeit nicht fertig verarbeitet.');
    textDocument = current;
    $('document-text-title').textContent = current.path.split('/').pop(); $('document-text-path').textContent = current.path;
    if (current.pages < 1) { $('document-text-message').textContent = 'Für dieses Dokument sind keine Seiten gespeichert.'; return; }
    await loadDocumentText(1);
  } catch (error) { if (request === textRequest) $('document-text-message').textContent = `Text konnte nicht geladen werden: ${error.message}`; }
}
function updateTextNavigation(loading) {
  const total = textDocument?.pages || 0;
  $('document-text-previous').disabled = loading || textPage <= 1;
  $('document-text-next').disabled = loading || textPage >= total;
  $('document-text-page').disabled = loading || !total;
  $('document-text-page-form').querySelector('button').disabled = loading || !total;
  $('document-text-page').max = Math.max(1, total); $('document-text-page').value = textPage;
  $('document-text-total').textContent = total ? `von ${total}` : '';
}
async function loadDocumentText(page) {
  if (!textDocument || page < 1 || page > textDocument.pages || !Number.isInteger(page)) return;
  const request = ++textRequest;
  textPage = page; updateTextNavigation(true);
  $('document-text-content').textContent = ''; $('document-text-content').setAttribute('aria-busy', 'true');
  $('document-text-empty').hidden = true; $('document-text-message').textContent = 'Gespeicherter Text wird geladen …';
  try {
    const stored = await api(`/api/documents/${textDocument.id}/pages/${page}`, {cache: 'no-store'});
    if (request !== textRequest) return;
    $('document-text-content').textContent = stored.text;
    $('document-text-content').scrollTop = 0;
    $('document-text-empty').hidden = stored.text.length !== 0;
    $('document-text-message').textContent = `Seite ${stored.number} von ${textDocument.pages} · ${stored.ocr ? 'Mit OCR erkannt' : 'Aus PDF-Text übernommen'}`;
  } catch (error) { if (request === textRequest) $('document-text-message').textContent = `Text konnte nicht geladen werden: ${error.message}`; }
  finally {
    if (request === textRequest) { updateTextNavigation(false); $('document-text-content').setAttribute('aria-busy', 'false'); }
  }
}
$('document-text-close').addEventListener('click', () => $('document-text-dialog').close());
$('document-text-dialog').addEventListener('close', () => {
  ++textRequest; textDocument = null; $('document-text-content').textContent = ''; $('document-text-content').setAttribute('aria-busy', 'false');
  (textTrigger?.isConnected ? textTrigger : $('show-documents')).focus(); textTrigger = null;
});
$('document-text-previous').addEventListener('click', () => loadDocumentText(textPage - 1));
$('document-text-next').addEventListener('click', () => loadDocumentText(textPage + 1));
$('document-text-page-form').addEventListener('submit', event => { event.preventDefault(); loadDocumentText(Number($('document-text-page').value)); });

let settingsRequest = 0;
function displaySettings(settings) {
  $('settings-root').textContent = settings.archive_root; $('settings-data').textContent = settings.data_directory;
  $('settings-source').textContent = settings.source_available ? 'Archivverzeichnis ist verfügbar.' : 'Archivverzeichnis fehlt oder wurde ausgetauscht. Der vorhandene Index bleibt erhalten.';
  $('settings-auto').checked = settings.scan_interval_seconds > 0;
  $('settings-minutes').value = settings.scan_interval_seconds > 0 ? settings.scan_interval_seconds / 60 : 15;
  $('settings-minutes').disabled = !$('settings-auto').checked;
}
async function loadSettings() {
  const request = ++settingsRequest;
  $('settings-save').disabled = true; $('settings-message').textContent = 'Einstellungen werden geladen …';
  try {
    const settings = await api('/api/settings');
    if (request !== settingsRequest) return;
    displaySettings(settings); $('settings-save').disabled = false; $('settings-message').textContent = '';
  } catch (error) { if (request === settingsRequest) $('settings-message').textContent = `Einstellungen konnten nicht geladen werden: ${error.message}`; }
}
$('settings-auto').addEventListener('change', () => { $('settings-minutes').disabled = !$('settings-auto').checked; });
$('settings-form').addEventListener('submit', async event => {
  event.preventDefault();
  const request = ++settingsRequest;
  $('settings-save').disabled = true; $('settings-message').textContent = 'Einstellungen werden gespeichert …';
  try {
    const seconds = $('settings-auto').checked ? Number($('settings-minutes').value) * 60 : 0;
    const settings = await api('/api/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({scan_interval_seconds: seconds})});
    if (request !== settingsRequest) return;
    displaySettings(settings);
    $('settings-message').textContent = seconds ? `Gespeichert. Das Archiv wird alle ${seconds / 60} Minuten abgeglichen.` : 'Gespeichert. Der automatische Abgleich ist deaktiviert.';
  } catch (error) { if (request === settingsRequest) $('settings-message').textContent = `Speichern fehlgeschlagen: ${error.message}`; }
  finally { if (request === settingsRequest) $('settings-save').disabled = false; }
});

async function initializeAccount() {
  try {
    const account = await api('/api/auth/me');
    csrfToken = account.csrf_token; permissions = account.permissions; currentUser = account.user;
    $('account-name').textContent = `${account.user.username} · ${account.user.role === 'admin' ? 'Administration' : 'Lesen'}`;
    $('account-menu').hidden = false;
    $('my-account-description').textContent = `${account.user.username} · ${account.user.role === 'admin' ? 'Administrator' : 'Benutzer mit Leserechten'}`;
    $('archive-settings').hidden = !permissions.manage_settings;
    $('user-management').hidden = !permissions.manage_users;
    $('scan').hidden = $('verify').hidden = !permissions.manage_imports;
    await refreshStatus(); statusTimer = setInterval(refreshStatus, 5000);
  } catch (error) { $('message').textContent = `Anmeldung konnte nicht geladen werden: ${error.message}`; }
}
$('logout').addEventListener('click', async () => {
  $('logout').disabled = true;
  try { await api('/api/auth/logout', {method: 'POST'}); clearInterval(statusTimer); window.location.replace('/login'); }
  catch (error) { $('message').textContent = `Abmelden fehlgeschlagen: ${error.message}`; $('logout').disabled = false; }
});
window.addEventListener('pageshow', event => { if (event.persisted) window.location.reload(); });
initializeAccount();
