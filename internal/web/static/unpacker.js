const $ = id => document.getElementById(id);
let info;

$('show').onchange = e => { $('pw').type = e.target.checked ? 'text' : 'password'; };
capsWarning($('pw'));
$('pw').addEventListener('input', () => {
  $('pwhint').hidden = !edgeSpace($('pw').value);
  $('pwhint').textContent = EDGE_NOTE;
});
$('pickkey').onclick = async () => { const p = await pick({ mode: 'file', title: 'Choose the keyfile', start: info.home }); if (p) $('keyfile').value = p; };
$('pickdest').onclick = async () => { const p = await pick({ mode: 'dir', title: 'Where should the files be extracted?', start: $('dest').value }); if (p) $('dest').value = p; };
$('quitslot').append(quitButton());

(async () => {
  info = await api('/api/info');
  $('dest').value = info.dest;
  $('keyblock').hidden = !info.needsKeyfile;
})().catch(e => fail(e.message));

function fail(text) { $('msg').className = 'error'; $('msg').textContent = text; }
function reset() { $('msg').className = ''; $('msg').textContent = ''; $('tree').replaceChildren(); }
function creds() { return { passphrase: $('pw').value, keyfile: $('keyfile').value }; }

$('preview').onclick = async () => {
  reset();
  if (!$('pw').value) return fail('Enter the passphrase.');
  $('preview').disabled = true;
  $('msg').textContent = 'Unlocking...';
  try {
    const items = await api('/api/list', creds());
    $('msg').textContent = `${items.length} entries`;
    const rows = items.map(i => el('tr', {}, el('td', {}, i.path + (i.isDir ? '/' : '')), el('td', { class: 'num' }, i.isDir ? '' : fmtSize(i.size))));
    $('tree').append(el('div', { class: 'tree' }, el('table', {}, el('tbody', {}, rows))));
  } catch (e) { fail(e.message); }
  $('preview').disabled = false;
};

$('go').onclick = async () => {
  reset();
  if (!$('pw').value) return fail('Enter the passphrase.');
  if (info.needsKeyfile && !$('keyfile').value) return fail('Choose the keyfile first.');
  $('go').disabled = $('preview').disabled = true;
  $('msg').textContent = 'Unlocking...';
  try {
    await api('/api/extract', { ...creds(), dest: $('dest').value, conflict: $('conflict').value, keepModes: $('modes').checked });
  } catch (e) { $('go').disabled = $('preview').disabled = false; return fail(e.message); }
  const draw = progressView($('prog'));
  const s = await watchJob(draw);
  $('go').disabled = $('preview').disabled = false;
  $('msg').textContent = '';
  if (s.state === 'error') return fail(s.message);
  draw(s, true);
  $('pw').value = '';
  $('msg').className = 'ok';
  $('msg').textContent = s.message;
  if (s.warnings && s.warnings.length) $('msg').append(el('div', { class: 'small' }, s.warnings.join('\n')));
};
