let chatContext = '', chatBusy = false, chatConfigured = false, chatStateRequest = 0;
$('show-chat').addEventListener('click', () => switchView('chat'));
function updateChatControls() {
  $('chat-question').disabled = chatBusy || !chatConfigured;
  $('chat-send').disabled = chatBusy || !chatConfigured;
  $('chat-new').disabled = chatBusy;
  $('chat-mode').disabled = chatBusy || !chatConfigured;
  for (const button of document.querySelectorAll('[data-question]')) button.disabled = chatBusy || !chatConfigured;
}
async function loadChatState() {
  const request = ++chatStateRequest;
  try {
    const state = await api('/api/chat/state');
    if (request !== chatStateRequest) return;
    chatConfigured = state.configured;
    $('chat-state').textContent = state.configured ? 'Der Agent kann im Archiv suchen und gespeicherte Seiten lesen.' : 'Der Archiv-Agent ist noch nicht eingerichtet. Sobald der zweite Container verbunden ist, kannst Du hier Fragen stellen.';
  } catch (error) { if (request === chatStateRequest) { chatConfigured = false; $('chat-state').textContent = `Agent-Verbindung konnte nicht geprüft werden: ${error.message}`; } }
  finally { if (request === chatStateRequest) updateChatControls(); }
}
function appendChatMessage(role, text, sources = []) {
  const card = element('article', '', `chat-entry ${role === 'user' ? 'chat-user' : 'chat-agent'}`);
  card.append(element('h2', role === 'user' ? 'Du' : 'Archiv-Agent'));
  const content = element('div', '', 'chat-content');
  // Only verified source IDs become links; model output is never interpreted as HTML.
  const pattern = /\[Dokument (\d+), Seite (\d+)\]/g;
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    content.append(document.createTextNode(text.slice(cursor, match.index)));
    const source = sources.find(item => String(item.document_id) === match[1] && String(item.page) === match[2]);
    if (source) { const link = element('a', match[0]); link.href = `/api/documents/${source.document_id}/pdf#page=${source.page}`; link.target = '_blank'; link.rel = 'noopener'; content.append(link); }
    else content.append(document.createTextNode(`${match[0]}${role === 'assistant' ? ' (nicht verifiziert)' : ''}`));
    cursor = match.index + match[0].length;
  }
  content.append(document.createTextNode(text.slice(cursor))); card.append(content);
  if (sources.length) {
    const details = element('details'); details.append(element('summary', 'Gelesene Quellen'));
    const list = element('ul');
    for (const source of sources) { const row = element('li'); const link = element('a', `${source.path} · Seite ${source.page}`); link.href = `/api/documents/${source.document_id}/pdf#page=${source.page}`; link.target = '_blank'; link.rel = 'noopener'; row.append(link); list.append(row); }
    details.append(list); card.append(details);
  }
  $('chat-messages').append(card); return card;
}
for (const button of document.querySelectorAll('[data-question]')) button.addEventListener('click', () => { $('chat-mode').value = 'research'; $('chat-question').value = button.dataset.question; $('chat-question').focus(); });
$('chat-new').addEventListener('click', () => { if (chatBusy) return; chatContext = ''; $('chat-mode').value = 'research'; $('chat-messages').replaceChildren(); $('chat-message').textContent = ''; $('chat-question').value = ''; if (chatConfigured) $('chat-question').focus(); });
$('chat-form').addEventListener('submit', async event => {
  event.preventDefault(); if (chatBusy || !chatConfigured) return;
  const question = $('chat-question').value.trim(); if (!question) return;
  const mode = $('chat-mode').value;
  if (mode === 'summary' && !chatContext) { $('chat-message').textContent = 'Recherchiere zuerst, bevor Du Ergebnisse zusammenführst.'; return; }
  chatBusy = true; updateChatControls(); $('chat-messages').setAttribute('aria-busy', 'true'); $('chat-message').textContent = mode === 'summary' ? 'Der Agent führt Deine bisherigen Ergebnisse zusammen …' : 'Der Agent sucht im Archiv und liest passende Seiten …';
  const row = appendChatMessage('user', question);
  try {
    const messages = [{role: 'user', content: question}];
    const answer = await api('/api/chat', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({messages, mode, context: chatContext})});
    appendChatMessage('assistant', answer.text, answer.sources || []);
    chatContext = answer.context;
    $('chat-question').value = ''; $('chat-message').textContent = answer.compacted ? 'Antwort erhalten. Frühere Ergebnisse wurden für die Fortsetzung verdichtet; der sichtbare Verlauf bleibt erhalten.' : 'Antwort erhalten. Du kannst eine weitere Teilfrage stellen oder Ergebnisse zusammenführen.';
  } catch (error) { row.remove(); $('chat-message').textContent = `Frage konnte nicht beantwortet werden: ${error.message}`; }
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
window.addEventListener('pagehide', () => { hideAgentToken(); chatContext = ''; $('chat-messages').replaceChildren(); });
