const $ = id => document.getElementById(id);
let info;

const browser = new Browser($('browser'), { mode: 'multi', onChange: refreshSel });

function refreshSel() {
  const paths = browser.value;
  $('chips').replaceChildren(...paths.map(p => el('span', { class: 'chip', title: p },
    el('span', {}, p), el('button', { type: 'button', 'aria-label': 'Remove', onclick: () => browser.deselect(p) }, '×'))));
  $('count').textContent = paths.length ? `${paths.length} selected` : 'Tick the files and folders you want. Folders keep their full structure.';
}

function targetObj() { const [os, arch] = $('target').value.split('/'); return { os, arch }; }
function outPreview() {
  const t = targetObj();
  const ext = t.os === 'windows' ? '.exe' : '';
  let n = $('outname').value.trim() || 'secure-archive';
  if (ext && !n.toLowerCase().endsWith(ext)) n += ext;
  $('outpreview').textContent = $('outdir').value ? 'Will create ' + $('outdir').value + (info.sep) + n : '';
}

$('show').onchange = e => { $('pw').type = $('pw2').type = e.target.checked ? 'text' : 'password'; };
const PW_HINT = 'The passphrase cannot be recovered. If you lose it, the data is gone.';
function checkPw() {
  const a = $('pw').value, b = $('pw2').value;
  const hint = $('pwhint'), match = $('pwmatch');
  hint.className = 'hint';
  if (edgeSpace(a) || edgeSpace(b)) { hint.className = 'error small'; hint.textContent = EDGE_NOTE; }
  else if (a && a.length < 12) hint.textContent = 'Short passphrases are easier to guess. Twelve or more characters is a good target.';
  else hint.textContent = PW_HINT;
  if (!b) match.textContent = '';
  else if (a === b) { match.className = 'small ok'; match.textContent = 'Both entries match.'; }
  else { match.className = 'small error'; match.textContent = 'The entries do not match yet.'; }
}
$('pw').oninput = $('pw2').oninput = checkPw;
capsWarning($('pw'));
capsWarning($('pw2'));
$('pickkey').onclick = async () => { const p = await pick({ mode: 'file', title: 'Choose a keyfile', start: info.home }); if (p) $('keyfile').value = p; };
$('clearkey').onclick = () => { $('keyfile').value = ''; };
$('pickout').onclick = async () => { const p = await pick({ mode: 'dir', title: 'Where should the executable be saved?', start: $('outdir').value }); if (p) { $('outdir').value = p; outPreview(); } };
$('outname').oninput = outPreview;
$('target').onchange = outPreview;
$('quitslot').append(quitButton());

(async () => {
  info = await api('/api/info');
  for (const t of info.targets) $('target').append(el('option', { value: t.os + '/' + t.arch, selected: t.os === info.host.os && t.arch === info.host.arch }, `${info.names[t.os] || t.os} ${t.arch}`));
  $('outdir').value = info.outDir;
  outPreview();
})().catch(e => { $('msg').className = 'error'; $('msg').textContent = e.message; });

$('go').onclick = async () => {
  const msg = $('msg');
  msg.className = ''; msg.textContent = '';
  const paths = browser.value;
  if (!paths.length) return fail('Pick at least one file or folder first.');
  if (!$('pw').value) return fail('Enter a passphrase.');
  if ($('pw').value !== $('pw2').value) return fail('The two passphrases do not match.');
  if ($('del').checked && !confirm('The originals will be permanently deleted once the new file is verified. Continue?')) return;

  $('go').disabled = true;
  const t = targetObj();
  try {
    await api('/api/pack', {
      paths, passphrase: $('pw').value, keyfile: $('keyfile').value, level: $('level').value,
      os: t.os, arch: t.arch, outDir: $('outdir').value, outName: $('outname').value.trim(),
      deleteOriginals: $('del').checked,
    });
  } catch (e) { $('go').disabled = false; return fail(e.message); }

  const draw = progressView($('prog'));
  const s = await watchJob(draw);
  $('go').disabled = false;
  if (s.state === 'error') return fail(s.message);
  draw(s, true);
  msg.className = 'ok';
  msg.textContent = s.message + '\nShare the SHA-256 with the recipient over a different channel, so they can confirm the file was not swapped.';
  if (s.warnings && s.warnings.length) msg.append(el('div', { class: 'small' }, s.warnings.join('\n')));
  $('pw').value = $('pw2').value = '';
  checkPw();
};
function fail(text) { $('msg').className = 'error'; $('msg').textContent = text; }
