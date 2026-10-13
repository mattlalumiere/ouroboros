const TOKEN = document.querySelector('meta[name=token]').content;

async function api(path, body) {
  const opt = { headers: { 'X-Token': TOKEN } };
  if (body !== undefined) {
    opt.method = 'POST';
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function el(tag, attrs = {}, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') n.className = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else if (v !== false && v != null) n.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) n.append(kid instanceof Node ? kid : document.createTextNode(kid ?? ''));
  return n;
}

function fmtSize(n) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i];
}

/*
 * File browser. mode is 'multi' (tick files and folders), 'dir' (the folder
 * being viewed is the choice) or 'file' (click one file).
 */
class Browser {
  constructor(host, { mode, start, onChange }) {
    this.mode = mode;
    this.onChange = onChange || (() => {});
    this.selected = new Set();
    this.showHidden = false;
    this.listing = null;
    this.host = host;
    this.go(start || '');
  }

  get value() {
    if (this.mode === 'dir') return this.listing ? this.listing.path : '';
    return [...this.selected];
  }

  join(name) {
    const { path, sep } = this.listing;
    return path.endsWith(sep) ? path + name : path + sep + name;
  }

  async go(path) {
    try {
      this.listing = await api('/api/fs?path=' + encodeURIComponent(path));
      this.error = '';
    } catch (e) {
      this.error = e.message;
    }
    this.render();
    this.onChange();
  }

  toggle(path) {
    this.selected.has(path) ? this.selected.delete(path) : this.selected.add(path);
    if (this.mode === 'file') this.selected = new Set(this.selected.has(path) ? [path] : []);
    this.render();
    this.onChange();
  }

  deselect(path) { this.selected.delete(path); this.render(); this.onChange(); }

  async newFolder() {
    const name = prompt('Name of the new folder');
    if (!name) return;
    try {
      const r = await api('/api/mkdir', { path: this.join(name) });
      this.go(r.path);
    } catch (e) { alert(e.message); }
  }

  render() {
    const L = this.listing;
    const pathBox = el('input', {
      type: 'text', value: L ? L.path : '', 'aria-label': 'Current folder',
      onkeydown: e => { if (e.key === 'Enter') this.go(e.target.value); },
    });
    const bar = el('div', { class: 'fb-bar' },
      el('button', { type: 'button', title: 'Parent folder', disabled: !L || !L.parent, onclick: () => this.go(L.parent) }, 'Up'),
      el('button', { type: 'button', onclick: () => this.go(L ? L.home : '') }, 'Home'),
      ...(L ? L.roots.map(r => el('button', { type: 'button', onclick: () => this.go(r) }, r)) : []),
      pathBox,
      el('label', { class: 'inline', style: 'margin:0' },
        el('input', { type: 'checkbox', checked: this.showHidden, onchange: e => { this.showHidden = e.target.checked; this.render(); } }), 'Hidden'),
      this.mode === 'dir' ? el('button', { type: 'button', onclick: () => this.newFolder() }, 'New folder') : '',
    );

    const list = el('div', { class: 'fb-list' });
    if (this.error) list.append(el('div', { class: 'fb-empty error' }, this.error));
    else if (L) {
      const rows = L.entries.filter(e => (this.showHidden || !e.name.startsWith('.')) && (this.mode !== 'dir' || e.isDir));
      if (!rows.length) list.append(el('div', { class: 'fb-empty' }, 'Nothing here'));
      for (const e of rows) {
        const full = this.join(e.name);
        const sel = this.selected.has(full);
        const box = this.mode === 'multi'
          ? el('input', { type: 'checkbox', checked: sel, 'aria-label': 'Select ' + e.name, onclick: ev => ev.stopPropagation(), onchange: () => this.toggle(full) })
          : '';
        const row = el('div', { class: 'fb-row' + (sel && this.mode === 'file' ? ' sel' : ''), onclick: () => (e.isDir ? this.go(full) : this.toggle(full)) },
          box, el('span', {}, e.isDir ? '\u{1F4C1}' : '\u{1F4C4}'), el('span', { class: 'name' }, e.name),
          el('span', { class: 'size' }, e.isDir ? '' : fmtSize(e.size)));
        list.append(row);
      }
    }
    this.host.replaceChildren(el('div', { class: 'fb' }, bar, list));
  }
}

/* Modal picker that resolves with a path, or null when cancelled. */
function pick({ mode, title, start }) {
  return new Promise(resolve => {
    const body = el('div');
    const choose = el('button', { class: 'primary', type: 'button' }, mode === 'dir' ? 'Use this folder' : 'Choose');
    const dlg = el('dialog', {}, el('h2', {}, title), body,
      el('div', { class: 'dlg-actions' }, el('button', { type: 'button', onclick: () => dlg.close() }, 'Cancel'), choose));
    let result = null;
    const b = new Browser(body, {
      mode, start,
      onChange: () => { choose.disabled = mode === 'file' ? b.selected.size === 0 : false; },
    });
    choose.onclick = () => { result = mode === 'dir' ? b.value : [...b.selected][0]; dlg.close(); };
    dlg.addEventListener('close', () => { dlg.remove(); resolve(result); });
    document.body.append(dlg);
    dlg.showModal();
  });
}

/* Shows job progress until it finishes, then resolves with the final state. */
function watchJob(onTick) {
  return new Promise(resolve => {
    const t = setInterval(async () => {
      try {
        const s = await api('/api/progress');
        onTick(s);
        if (s.state === 'done' || s.state === 'error') { clearInterval(t); resolve(s); }
      } catch (e) {
        clearInterval(t);
        resolve({ state: 'error', message: e.message, warnings: [] });
      }
    }, 250);
  });
}

function progressView(host) {
  const fill = el('div');
  const text = el('div', { class: 'small' });
  host.replaceChildren(el('div', { class: 'bar' }, fill), text);
  return s => {
    const p = s.progress || {};
    const pct = p.Total ? Math.min(100, (p.Bytes / p.Total) * 100) : 0;
    fill.style.width = pct + '%';
    text.textContent = `${p.Files || 0} files, ${fmtSize(p.Bytes || 0)}${p.Total ? ' of ' + fmtSize(p.Total) : ''}${p.Current ? ' • ' + p.Current : ''}`;
  };
}

function quitButton() {
  return el('button', {
    type: 'button',
    onclick: async () => {
      try { await api('/api/quit', {}); } catch (e) { /* the server closes the connection */ }
      document.body.replaceChildren(el('main', {}, el('h1', {}, 'All done'), el('p', { class: 'sub' }, 'You can close this tab.')));
    },
  }, 'Quit');
}
