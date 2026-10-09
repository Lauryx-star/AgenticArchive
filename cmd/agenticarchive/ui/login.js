'use strict';
const $ = id => document.getElementById(id);
let setupRequired = false;
try { const saved = localStorage.getItem('theme'); if (['light', 'dark'].includes(saved)) { document.documentElement.dataset.theme = saved; $('theme').value = saved; } } catch {}
$('theme').addEventListener('change', () => {
  const value = $('theme').value;
  if (value === 'auto') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = value;
  try { localStorage.setItem('theme', value); } catch {}
});
async function request(url, input) {
  const response = await fetch(url, input ? {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(input), credentials: 'same-origin', cache: 'no-store'} : {cache: 'no-store'});
  const body = await response.json();
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}
async function loadState() {
  try {
    const state = await request('/api/auth/state'); setupRequired = state.setup_required;
    $('login-title').textContent = setupRequired ? 'Konto einrichten.' : 'Anmelden.';
    $('login-intro').textContent = setupRequired ? 'Lege Dein Administratorkonto an. Bis dahin bleibt das Archiv gesperrt.' : 'Melde Dich an, um auf Deine Dokumente zuzugreifen.';
    $('setup-fields').hidden = $('password-confirm-field').hidden = $('password-hint').hidden = !setupRequired;
    $('setup-code').required = $('password-confirm').required = setupRequired;
    $('password').autocomplete = setupRequired ? 'new-password' : 'current-password';
    $('password').minLength = setupRequired ? 15 : 1;
    $('login-submit').textContent = setupRequired ? 'Konto einrichten' : 'Anmelden';
    $('login-message').textContent = !setupRequired && new URLSearchParams(window.location.search).get('password_changed') === '1' ? 'Dein Passwort wurde geändert. Bitte melde Dich mit dem neuen Passwort an.' : ''; $('login-form').hidden = false;
  } catch (error) { $('login-message').textContent = `Anmeldung nicht verfügbar: ${error.message} Bitte die Seite neu laden.`; }
}
$('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (setupRequired && $('password').value !== $('password-confirm').value) { $('login-message').textContent = 'Die Passwörter stimmen nicht überein.'; return; }
  $('login-submit').disabled = true;
  $('login-message').textContent = setupRequired ? 'Konto wird eingerichtet …' : 'Anmeldung läuft …';
  const credentials = {username: $('username').value, password: $('password').value};
  try {
    if (setupRequired) {
      await request('/api/auth/setup', {...credentials, setup_code: $('setup-code').value});
      $('setup-code').value = ''; $('password-confirm').value = '';
      // Setup is complete even if the subsequent login loses its response.
      await loadState();
    }
    await request('/api/auth/login', credentials);
    $('password').value = '';
    window.location.replace('/');
  } catch (error) { $('login-message').textContent = error.message; }
  finally { $('login-submit').disabled = false; }
});
loadState();
