'use strict';
// Shares the authenticated API and account state from app.js.
let userListRequest = 0, resetPasswordRequest = 0, passwordTarget = null, passwordTrigger = null;

async function loadUsers() {
  const request = ++userListRequest;
  $('users-list').setAttribute('aria-busy', 'true');
  try {
    const result = await api('/api/users');
    if (request !== userListRequest) return;
    const admins = result.users.filter(user => user.role === 'admin').length;
    $('users-list').replaceChildren();
    for (const user of result.users) {
      const card = element('article', '', 'user-card');
      const own = user.id === currentUser.id, lastAdmin = user.role === 'admin' && admins === 1;
      card.append(element('h3', `${user.username}${own ? ' · Dein Konto' : ''}`));
      const form = element('form', '', 'user-role-form');
      const label = element('label', 'Berechtigung'); label.htmlFor = `user-role-${user.id}`;
      const select = document.createElement('select'); select.id = label.htmlFor;
      for (const [value, text] of [['reader', 'Benutzer · Lesen'], ['admin', 'Administrator']]) {
        const option = element('option', text); option.value = value; select.append(option);
      }
      select.value = user.role; select.disabled = lastAdmin;
      const save = element('button', 'Berechtigung speichern', 'secondary'); save.type = 'submit'; save.disabled = true;
      select.addEventListener('change', () => { save.disabled = select.value === user.role; });
      form.append(label, select, save);
      const message = element('p', '', 'hint'); message.setAttribute('role', 'status'); message.setAttribute('aria-live', 'polite');
      form.addEventListener('submit', async event => {
        event.preventDefault(); save.disabled = select.disabled = true;
        try {
          await api(`/api/users/${user.id}/role`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({role: select.value})});
          if (own) { window.location.reload(); return; }
          $('users-message').textContent = `Berechtigung für ${user.username} gespeichert.`;
          await loadUsers();
        } catch (error) { message.textContent = error.message; select.disabled = false; save.disabled = select.value === user.role; }
      });
      card.append(form, message);
      if (lastAdmin) card.append(element('p', 'Mindestens ein Administratorkonto muss erhalten bleiben.', 'hint'));
      if (own) card.append(element('p', 'Dein eigenes Passwort änderst Du oben unter „Mein Konto“.', 'hint'));
      else {
        const reset = element('button', 'Neues Passwort vergeben', 'secondary'); reset.type = 'button';
        reset.addEventListener('click', () => openPasswordReset(user, reset)); card.append(reset);
      }
      $('users-list').append(card);
    }
  } catch (error) { if (request === userListRequest) $('users-message').textContent = `Benutzer konnten nicht geladen werden: ${error.message}`; }
  finally { if (request === userListRequest) $('users-list').setAttribute('aria-busy', 'false'); }
}

$('my-password-form').addEventListener('submit', async event => {
  event.preventDefault();
  if ($('new-password').value !== $('new-password-confirm').value) { $('my-password-message').textContent = 'Die neuen Passwörter stimmen nicht überein.'; return; }
  $('my-password-save').disabled = true; $('my-password-message').textContent = 'Passwort wird geändert …';
  // Avoid a status poll navigating away between session revocation and this response.
  changingPassword = true;
  clearInterval(statusTimer);
  try {
    await api('/api/auth/password', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({current_password: $('current-password').value, new_password: $('new-password').value})});
    $('my-password-form').reset();
    window.location.replace('/login?password_changed=1');
  } catch (error) {
    changingPassword = false;
    $('my-password-message').textContent = error.message;
    statusTimer = setInterval(refreshStatus, 5000);
  } finally { $('my-password-save').disabled = false; }
});

$('create-user-form').addEventListener('submit', async event => {
  event.preventDefault();
  if ($('create-password').value !== $('create-password-confirm').value) { $('create-user-message').textContent = 'Die Passwörter stimmen nicht überein.'; return; }
  $('create-user-save').disabled = true; $('create-user-message').textContent = 'Benutzerkonto wird angelegt …';
  try {
    const user = await api('/api/users', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({username: $('create-username').value, password: $('create-password').value, role: $('create-role').value})});
    $('create-user-form').reset();
    $('create-user-message').textContent = `Benutzerkonto ${user.username} angelegt.`;
    await loadUsers();
  } catch (error) { $('create-user-message').textContent = error.message; }
  finally { $('create-user-save').disabled = false; }
});

function openPasswordReset(user, trigger) {
  passwordTarget = user; passwordTrigger = trigger; ++resetPasswordRequest;
  $('reset-password-form').reset(); $('reset-password-save').disabled = false;
  $('user-password-name').textContent = `Benutzerkonto: ${user.username}`;
  $('reset-password-message').textContent = '';
  $('user-password-dialog').showModal();
}
$('user-password-close').addEventListener('click', () => $('user-password-dialog').close());
$('user-password-dialog').addEventListener('close', () => {
  ++resetPasswordRequest; passwordTarget = null; $('reset-password-form').reset();
  if (passwordTrigger?.isConnected) passwordTrigger.focus(); passwordTrigger = null;
});
$('reset-password-form').addEventListener('submit', async event => {
  event.preventDefault();
  if (!passwordTarget) return;
  if ($('reset-password').value !== $('reset-password-confirm').value) { $('reset-password-message').textContent = 'Die Passwörter stimmen nicht überein.'; return; }
  const user = passwordTarget, request = ++resetPasswordRequest;
  $('reset-password-save').disabled = true; $('reset-password-message').textContent = 'Passwort wird neu vergeben …';
  try {
    await api(`/api/users/${user.id}/password`, {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({password: $('reset-password').value})});
    $('users-message').textContent = `Neues Passwort für ${user.username} vergeben. Alle Sitzungen dieses Kontos wurden abgemeldet.`;
    if (request === resetPasswordRequest) $('user-password-dialog').close();
  } catch (error) { if (request === resetPasswordRequest) $('reset-password-message').textContent = error.message; }
  finally { if (request === resetPasswordRequest) $('reset-password-save').disabled = false; }
});
