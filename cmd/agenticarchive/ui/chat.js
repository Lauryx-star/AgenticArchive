let chatCurrent = null, savedChats = [], selectedChats = new Set(), chatNextBefore = 0, chatListRequest = 0, chatBusy = false, chatConfigured = false, chatStateRequest = 0;
$('show-chat').addEventListener('click', () => switchView('chat'));
function updateChatControls() {
  $('chat-question').disabled = chatBusy || !chatConfigured;
  $('chat-send').disabled = chatBusy || !chatConfigured;
  $('chat-new').disabled = chatBusy;
  $('chat-mode').disabled = chatBusy || !chatConfigured;
  $('chat-merge').disabled = chatBusy || selectedChats.size < 2 || selectedChats.size > 5;
  $('chat-more').disabled = chatBusy; $('chat-import-save').disabled = chatBusy; $('chat-import-text').disabled = chatBusy;
  for (const id of ['chat-notes-save', 'chat-name', 'chat-notes', 'chat-refresh', 'chat-delete']) $(id).disabled = chatBusy || !chatCurrent;
  for (const control of document.querySelectorAll('#saved-chats-list input, #saved-chats-list button')) control.disabled = chatBusy;
  for (const button of document.querySelectorAll('[data-question]')) button.disabled = chatBusy || !chatConfigured;
}
async function loadChatState() {
  const request = ++chatStateRequest;
  try {
    const state = await api('/api/chat/state');
    if (request !== chatStateRequest) return;
    chatConfigured = state.configured;
    if (!chatBusy) {
      await loadSavedChats(true);
      if (!chatCurrent && savedChats.length) {
        let remembered = null;
        try { remembered = Number(sessionStorage.getItem(`archive-chat-${currentUser.id}`)); } catch {}
        await openSavedChat(remembered || savedChats[0].id).catch(async () => { await openSavedChat(savedChats[0].id); });
      }
    }
    $('chat-state').textContent = state.configured ? 'Der Agent kann im Archiv suchen und gespeicherte Seiten lesen.' : 'Der Archiv-Agent ist noch nicht eingerichtet. Sobald der zweite Container verbunden ist, kannst Du hier Fragen stellen.';
  } catch (error) { if (request === chatStateRequest) { chatConfigured = false; $('chat-state').textContent = `Agent-Verbindung konnte nicht geprüft werden: ${error.message}`; } }
  finally { if (request === chatStateRequest) updateChatControls(); }
}
function appendChatMessage(role, text, sources = [], target = $('chat-messages')) {
  const card = element('article', '', `chat-entry ${role === 'user' ? 'chat-user' : 'chat-agent'}`);
  card.append(element('h2', role === 'user' ? 'Du' : 'Archiv-Agent'));
  const content = element('div', '', 'chat-content');
  // Only verified source IDs become links; model output is never interpreted as HTML.
  const pattern = /\[Dokument (\d+), Seite (\d+)\]/g;
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    content.append(document.createTextNode(text.slice(cursor, match.index)));
    const source = sources.find(item => String(item.document_id) === match[1] && String(item.page) === match[2]);
    if (source && !source.unavailable && !source.imported) { const link = element('a', match[0]); link.href = `/api/documents/${source.document_id}/pdf#page=${source.page}`; link.target = '_blank'; link.rel = 'noopener'; content.append(link); }
    else content.append(document.createTextNode(`${match[0]}${source?.unavailable ? ' (Quelle fehlt oder wurde verändert)' : source?.imported ? ' (übernommener Verweis, nicht neu geprüft)' : role === 'assistant' ? ' (nicht verifiziert)' : ''}`));
    cursor = match.index + match[0].length;
  }
  content.append(document.createTextNode(text.slice(cursor))); card.append(content);
  if (sources.length) {
    const details = element('details'); details.append(element('summary', 'Gelesene Quellen'));
    const list = element('ul');
    for (const source of sources) { const row = element('li'); if (source.unavailable) { row.textContent = `${source.path} · Seite ${source.page} (Quelle fehlt oder wurde verändert)`; list.append(row); continue; } const link = element('a', `${source.path} · Seite ${source.page}${source.imported ? ' · übernommener Verweis' : ''}`); link.href = `/api/documents/${source.document_id}/pdf#page=${source.page}`; link.target = '_blank'; link.rel = 'noopener'; row.append(link); list.append(row); }
    details.append(list); card.append(details);
  }
  target.append(card); return card;
}
for (const button of document.querySelectorAll('[data-question]')) button.addEventListener('click', () => { $('chat-mode').value = 'research'; $('chat-question').value = button.dataset.question; $('chat-question').focus(); });
function rememberChat() { try { sessionStorage.setItem(`archive-chat-${currentUser.id}`, String(chatCurrent.id)); } catch {} }
function showSavedChat(chat) {
  chatCurrent = chat; rememberChat(); $('chat-messages').replaceChildren();
  for (const message of chat.messages || []) { const row = appendChatMessage(message.role, message.content, message.sources || []); row.id = `chat-message-${message.id}`; }
  $('chat-saved-info').hidden = false; $('chat-current-title').textContent = chat.title;
  $('chat-name').value = chat.title; $('chat-notes').value = chat.notes || '';
  $('chat-imports').textContent = (chat.imports || []).length ? `Übernommene Ergebnisse aus: ${chat.imports.map(item => item.title).join(', ')}. Stand zum Zeitpunkt der Zusammenführung.` : '';
  $('chat-results').replaceChildren();
  for (const result of chat.results || []) {
    const details = element('details'); details.append(element('summary', `${result.partial ? 'Teilergebnis' : 'Rechercheergebnis'} · ${new Date(result.created).toLocaleString('de-DE')} · ${result.sources.length} Quellen`));
    appendChatMessage('assistant', result.content, result.sources, details);
    if (result.open_questions) details.append(element('p', result.open_questions, 'chat-content'));
    $('chat-results').append(details);
  }
  updateChatControls();
}
async function openSavedChat(id) { const chat = await api(`/api/chats/${id}`); showSavedChat(chat); renderSavedChats(); }
function renderSavedChats() {
  $('saved-chats-list').replaceChildren();
  if (!savedChats.length) $('saved-chats-list').append(element('p', 'Du hast noch keine gespeicherten Recherchen.', 'hint'));
  for (const chat of savedChats) {
    const row = element('div', '', 'saved-chat-row');
    const label = element('label', '', 'saved-chat-choice'); const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.checked = selectedChats.has(chat.id); checkbox.setAttribute('aria-label', `${chat.title} zum Zusammenführen auswählen`);
    checkbox.addEventListener('change', () => { if (checkbox.checked) selectedChats.add(chat.id); else selectedChats.delete(chat.id); updateChatControls(); }); label.append(checkbox);
    const open = element('button', `${chat.title}${chatCurrent?.id === chat.id ? ' · geöffnet' : ''}`, 'secondary'); open.type = 'button'; open.addEventListener('click', () => runChatAction(() => openSavedChat(chat.id)));
    row.append(label, open); $('saved-chats-list').append(row);
  }
  $('chat-more').hidden = !chatNextBefore; updateChatControls();
}
async function loadSavedChats(reset = true) {
  const request = ++chatListRequest;
  const result = await api(`/api/chats${!reset && chatNextBefore ? `?before=${chatNextBefore}` : ''}`);
  if (request !== chatListRequest) return;
  savedChats = reset ? result.chats : [...savedChats, ...result.chats]; chatNextBefore = result.next_before; renderSavedChats();
}
async function runChatAction(action) {
  if (chatBusy) return; chatBusy = true; updateChatControls(); $('chat-message').textContent = '';
  try { await action(); } catch (error) { $('chat-message').textContent = error.message; }
  finally { chatBusy = false; updateChatControls(); }
}
async function createSavedChat(title = 'Neue Recherche') {
  const chat = await api('/api/chats', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({title})}); showSavedChat(chat); await loadSavedChats(); return chat;
}
$('chat-new').addEventListener('click', () => runChatAction(async () => { await createSavedChat(); $('chat-mode').value = 'research'; $('chat-question').value = ''; if (chatConfigured) $('chat-question').focus(); }));
$('chat-import-form').addEventListener('submit', event => { event.preventDefault(); runChatAction(async () => {
  const chat = await api('/api/chats', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({title: 'Übernommener bisheriger Chat', transcript: $('chat-import-text').value.trim()})});
  showSavedChat(chat); await loadSavedChats(); $('chat-import-text').value = ''; $('chat-message').textContent = 'Früherer Verlauf gespeichert. Übernommene Quellenverweise sind als nicht neu geprüft gekennzeichnet.';
}); });
$('chat-refresh').addEventListener('click', () => runChatAction(() => openSavedChat(chatCurrent.id)));
$('chat-more').addEventListener('click', () => runChatAction(() => loadSavedChats(false)));
$('chat-merge').addEventListener('click', () => runChatAction(async () => {
  const chat = await api('/api/chats/merge', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({ids: [...selectedChats]})});
  selectedChats.clear(); showSavedChat(chat); await loadSavedChats(); $('chat-mode').value = 'summary'; $('chat-question').value = 'Führe die übernommenen Rechercheergebnisse mit ihren Quellen zusammen und nenne weiterhin die offenen Fragen und Lücken.';
  $('chat-message').textContent = 'Ergebnisse übernommen. Du kannst jetzt die Zusammenfassung senden oder eine eigene Frage stellen.';
}));
$('chat-notes-form').addEventListener('submit', event => { event.preventDefault(); runChatAction(async () => {
  const result = await api(`/api/chats/${chatCurrent.id}`, {method: 'PATCH', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({title: $('chat-name').value.trim(), notes: $('chat-notes').value.trim(), version: chatCurrent.version})});
  chatCurrent.version = result.version; chatCurrent.title = $('chat-name').value.trim(); chatCurrent.notes = $('chat-notes').value.trim(); $('chat-current-title').textContent = chatCurrent.title; await loadSavedChats(); $('chat-message').textContent = 'Titel und Recherchehinweise gespeichert.';
}); });
$('chat-delete').addEventListener('click', () => { if (chatBusy || !chatCurrent || !window.confirm('Diesen Chat mit seinem Verlauf, Ergebnissen und Hinweisen endgültig löschen?')) return; runChatAction(async () => {
  await api(`/api/chats/${chatCurrent.id}`, {method: 'DELETE'}); selectedChats.delete(chatCurrent.id);
  try { sessionStorage.removeItem(`archive-chat-${currentUser.id}`); } catch {}
  chatCurrent = null; $('chat-saved-info').hidden = true; $('chat-messages').replaceChildren(); await loadSavedChats(); if (savedChats.length) await openSavedChat(savedChats[0].id);
  $('chat-message').textContent = 'Chat gelöscht.';
}); });
$('chat-form').addEventListener('submit', async event => {
  event.preventDefault(); if (chatBusy || !chatConfigured) return;
  const question = $('chat-question').value.trim(); if (!question) return;
  const mode = $('chat-mode').value;
  if (mode === 'summary' && (!chatCurrent || !(chatCurrent.messages || []).length)) { $('chat-message').textContent = 'Recherchiere zuerst, bevor Du Ergebnisse zusammenführst.'; return; }
  chatBusy = true; updateChatControls(); $('chat-messages').setAttribute('aria-busy', 'true'); $('chat-message').textContent = mode === 'summary' ? 'Der Agent führt Deine bisherigen Ergebnisse zusammen …' : 'Der Agent sucht im Archiv und liest passende Seiten …';
  let row = null, answerSaved = false;
  try {
    if (!chatCurrent) await createSavedChat(Array.from(question.replace(/\s+/g, ' ')).slice(0, 120).join(''));
    row = appendChatMessage('user', question);
    const messages = [{role: 'user', content: question}];
    const answer = await api('/api/chat', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({messages, mode, chat_id: chatCurrent.id, version: chatCurrent.version})});
    answerSaved = true; appendChatMessage('assistant', answer.text, answer.sources || []);
    chatCurrent.version = answer.version;
    await openSavedChat(answer.chat_id); await loadSavedChats();
    $('chat-question').value = ''; $('chat-message').textContent = answer.compacted ? 'Antwort erhalten. Frühere Ergebnisse wurden für die Fortsetzung verdichtet; der sichtbare Verlauf bleibt erhalten.' : 'Antwort erhalten. Du kannst eine weitere Teilfrage stellen oder Ergebnisse zusammenführen.';
  } catch (error) { if (!answerSaved) row?.remove(); $('chat-message').textContent = answerSaved ? `Antwort gespeichert. Die Ansicht konnte nicht vollständig aktualisiert werden: ${error.message}. Lade den Chat neu.` : `Frage konnte nicht beantwortet werden: ${error.message}`; }
  finally { chatBusy = false; updateChatControls(); $('chat-messages').setAttribute('aria-busy', 'false'); if (!$('chat-view').hidden && chatConfigured) $('chat-question').focus(); }
});

let tokenListRequest = 0, displayedTokenID = null;
function hideAgentToken() { $('agent-token-secret').value = ''; $('agent-token-secret-panel').hidden = true; displayedTokenID = null; }
async function loadAgentTokens() {
  const request = ++tokenListRequest;
  try {
    const result = await api('/api/auth/tokens'); if (request !== tokenListRequest) return;
    $('agent-tokens-list').replaceChildren();
    for (const token of result.tokens) {
      const row = element('article', '', 'user-card'); row.append(element('h3', token.name), element('p', `Leserechte · Gültig bis ${new Date(token.expires).toLocaleString('de-DE')}`, 'hint'));
      const revoke = element('button', 'Widerrufen', 'secondary'); revoke.type = 'button';
      revoke.addEventListener('click', async () => { revoke.disabled = true; try { await api(`/api/auth/tokens/${token.id}`, {method: 'DELETE'}); if (displayedTokenID === token.id) hideAgentToken(); await loadAgentTokens(); } catch (error) { $('agent-tokens-message').textContent = error.message; revoke.disabled = false; } });
      row.append(revoke); $('agent-tokens-list').append(row);
    }
    $('agent-tokens-message').textContent = result.tokens.length ? `${result.tokens.length} aktive Lese-Tokens` : 'Du hast noch keine aktiven Agent-Tokens.';
  } catch (error) { if (request === tokenListRequest) $('agent-tokens-message').textContent = `Tokens konnten nicht geladen werden: ${error.message}`; }
}
$('agent-token-hide').addEventListener('click', hideAgentToken);
$('agent-token-details').addEventListener('toggle', () => { if (!$('agent-token-details').open) hideAgentToken(); });
$('agent-token-form').addEventListener('submit', async event => {
  event.preventDefault(); $('agent-token-create').disabled = true; hideAgentToken();
  try {
    const result = await api('/api/auth/tokens', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({name: $('agent-token-name').value.trim(), days: Number($('agent-token-days').value)})});
    await loadAgentTokens();
    if ($('settings-view').hidden || !$('agent-token-details').open) { $('agent-tokens-message').textContent = 'Token erstellt. Es wurde bereits ausgeblendet. Widerrufe es und erstelle bei Bedarf ein neues.'; return; }
    displayedTokenID = result.token.id; $('agent-token-secret').value = result.secret; $('agent-token-secret-panel').hidden = false; $('agent-token-secret').focus(); $('agent-token-secret').select(); $('agent-token-name').value = '';
  } catch (error) { $('agent-tokens-message').textContent = `Token konnte nicht erstellt werden: ${error.message}`; }
  finally { $('agent-token-create').disabled = false; }
});
window.addEventListener('pagehide', () => { hideAgentToken(); chatCurrent = null; savedChats = []; selectedChats.clear(); $('chat-messages').replaceChildren(); });
