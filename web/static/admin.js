(() => {
'use strict';

const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
function el(tag, props = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v);
  }
  e.append(...kids);
  return e;
}
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.hidden = false;
  clearTimeout(toast.t); toast.t = setTimeout(() => { t.hidden = true; }, 2000);
}

async function api(url, opts = {}) {
  const o = { credentials: 'same-origin', ...opts, headers: { 'X-Requested-With': 'fetch', ...(opts.headers || {}) } };
  if (o.body && !(o.body instanceof FormData)) { o.headers['Content-Type'] = 'application/json'; o.body = JSON.stringify(o.body); }
  const r = await fetch(url, o);
  let data = null; try { data = await r.json(); } catch { /* không có JSON */ }
  if (!r.ok) { const e = new Error((data && data.error) || ('Lỗi ' + r.status)); e.status = r.status; throw e; }
  return data;
}

let topics = [], cards = [];
let draft = null;   // flashcard đang soạn
let editingTopic = null;

// ---------------------------------------------------------------- đăng nhập
async function boot() {
  let me;
  try { me = await api('/api/me'); } catch { me = { admin: false }; }
  $('#loginView').hidden = !!me.admin;
  $('#panelView').hidden = !me.admin;
  $('#logout').hidden = !me.admin;
  if (me.admin) await loadData(); else $('#lu').focus();
}

$('#loginForm').addEventListener('submit', async e => {
  e.preventDefault();
  $('#loginErr').textContent = '';
  try {
    await api('/api/login', { method: 'POST', body: { username: $('#lu').value, password: $('#lp').value } });
    $('#lp').value = '';
    await boot();
  } catch (err) { $('#loginErr').textContent = err.message; }
});

$('#logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST' }); } catch { /* bỏ qua */ }
  boot();
});

$$('.tabs button').forEach(b => b.addEventListener('click', () => {
  $$('.tabs button').forEach(x => x.classList.toggle('active', x === b));
  for (const t of ['cards', 'topics', 'system']) $('#tab-' + t).hidden = (t !== b.dataset.tab);
}));

async function loadData() {
  try {
    [topics, cards] = await Promise.all([api('/api/topics'), api('/api/cards')]);
  } catch (err) {
    if (err.status === 401) return boot();
    toast(err.message);
  }
  renderFilter(); renderCards(); renderTopics();
}

// ---------------------------------------------------------------- flashcard
function renderFilter() {
  const sel = $('#filterTopic'); const v = sel.value;
  sel.replaceChildren(el('option', { value: '', text: 'Tất cả chủ đề' }), ...topics.map(t => el('option', { value: t.id, text: `${t.name} (${t.card_count})` })));
  sel.value = topics.some(t => String(t.id) === v) ? v : '';
}
$('#filterTopic').addEventListener('change', renderCards);

function renderCards() {
  const f = $('#filterTopic').value;
  const list = cards.filter(c => !f || String(c.topic_id) === f);
  $('#cardCount').textContent = `${list.length} flashcard`;
  const box = $('#cardList'); box.replaceChildren();
  if (!list.length) {
    box.append(el('div', { class: 'empty', text: topics.length ? 'Chưa có flashcard. Chọn "Thêm flashcard" để tạo thẻ đầu tiên.' : 'Hãy tạo chủ đề trước, sau đó thêm flashcard.' }));
    return;
  }
  for (const c of list) {
    box.append(el('div', { class: 'row' },
      el('img', { src: c.image, alt: '', loading: 'lazy' }),
      el('div', { class: 'txt' },
        el('div', { class: 'small muted', text: c.topic_name + (c.keywords ? ' · ' + c.keywords : '') }),
        el('div', { class: 'snip', text: c.back })),
      el('div', { class: 'acts' },
        el('button', { class: 'btn ghost small', type: 'button', text: 'Sửa', onclick: () => openCard(c) }),
        el('button', { class: 'btn danger small', type: 'button', text: 'Xóa', onclick: () => delCard(c) }))));
  }
}

function openCard(c) {
  if (!topics.length) { toast('Hãy tạo chủ đề trước'); return; }
  draft = c
    ? { id: c.id, topic_id: c.topic_id, image: c.image.replace(/^\//, ''), back: c.back, keywords: c.keywords }
    : { id: null, topic_id: Number($('#filterTopic').value) || topics[0].id, image: '', back: '', keywords: '' };
  $('#cardDlgTitle').textContent = c ? 'Sửa flashcard' : 'Thêm flashcard';
  $('#cTopic').replaceChildren(...topics.map(t => el('option', { value: t.id, text: t.name })));
  $('#cTopic').value = draft.topic_id;
  $('#cBack').value = draft.back; $('#cKeys').value = draft.keywords; $('#cFile').value = '';
  $('#cardErr').textContent = '';
  $('#pvCard').classList.remove('flipped');
  updatePreview();
  $('#cardDlg').showModal();
}

function updatePreview() {
  const img = $('#pvImg');
  if (draft.image) img.src = '/' + draft.image; else img.removeAttribute('src');
  $('#pvText').textContent = $('#cBack').value || 'Nội dung mặt sau sẽ hiện ở đây';
  $('#pvKeys').textContent = $('#cKeys').value;
}
$('#cBack').addEventListener('input', updatePreview);
$('#cKeys').addEventListener('input', updatePreview);
$('#pvCard').addEventListener('click', () => $('#pvCard').classList.toggle('flipped'));
$('#addCard').addEventListener('click', () => openCard(null));
$('#cardCancel').addEventListener('click', () => $('#cardDlg').close());

// thu nhỏ ảnh lớn từ điện thoại trước khi tải lên
async function shrink(file, max = 1600) {
  if (!/^image\/(jpeg|png|webp)$/.test(file.type) || !window.createImageBitmap) return file;
  let bmp; try { bmp = await createImageBitmap(file); } catch { return file; }
  const scale = Math.min(1, max / Math.max(bmp.width, bmp.height));
  if (scale === 1 && file.size < 1.5e6) return file;
  const c = document.createElement('canvas');
  c.width = Math.round(bmp.width * scale); c.height = Math.round(bmp.height * scale);
  const ctx = c.getContext('2d');
  ctx.fillStyle = '#fff'; ctx.fillRect(0, 0, c.width, c.height);
  ctx.drawImage(bmp, 0, 0, c.width, c.height);
  const blob = await new Promise(r => c.toBlob(r, 'image/jpeg', 0.86));
  return blob ? new File([blob], 'image.jpg', { type: 'image/jpeg' }) : file;
}

$('#cFile').addEventListener('change', async e => {
  const f = e.target.files[0]; if (!f) return;
  $('#cardErr').textContent = ''; $('#cardSave').disabled = true;
  try {
    const small = await shrink(f);
    const fd = new FormData(); fd.append('image', small, small.name || 'image.jpg');
    const r = await api('/api/admin/upload', { method: 'POST', body: fd });
    draft.image = r.path; updatePreview();
  } catch (err) { $('#cardErr').textContent = err.message; }
  finally { $('#cardSave').disabled = false; e.target.value = ''; }
});

$('#cardForm').addEventListener('submit', async e => {
  e.preventDefault();
  $('#cardErr').textContent = '';
  const body = { topic_id: Number($('#cTopic').value), image: draft.image, back: $('#cBack').value, keywords: $('#cKeys').value };
  try {
    if (draft.id) await api('/api/admin/cards/' + draft.id, { method: 'PUT', body });
    else await api('/api/admin/cards', { method: 'POST', body });
    $('#cardDlg').close();
    toast('Đã lưu flashcard');
    await loadData();
  } catch (err) {
    if (err.status === 401) { $('#cardDlg').close(); return boot(); }
    $('#cardErr').textContent = err.message;
  }
});

async function delCard(c) {
  if (!confirm('Xóa flashcard này? Hành động không thể hoàn tác.\n\n' + c.back.slice(0, 80))) return;
  try { await api('/api/admin/cards/' + c.id, { method: 'DELETE' }); toast('Đã xóa flashcard'); await loadData(); }
  catch (err) { toast(err.message); }
}

// ---------------------------------------------------------------- chủ đề
function renderTopics() {
  const box = $('#topicList'); box.replaceChildren();
  if (!topics.length) { box.append(el('div', { class: 'empty', text: 'Chưa có chủ đề. Chọn "Thêm chủ đề" để bắt đầu.' })); return; }
  for (const t of topics) {
    box.append(el('div', { class: 'row topic' },
      el('div', { class: 'txt' },
        el('h3', { text: t.name }),
        el('div', { class: 'small muted', text: `${t.card_count} flashcard` + (t.description ? ' · ' + t.description : '') })),
      el('div', { class: 'acts' },
        el('button', { class: 'btn ghost small', type: 'button', text: 'Sửa', onclick: () => openTopic(t) }),
        el('button', { class: 'btn danger small', type: 'button', text: 'Xóa', onclick: () => delTopic(t) }))));
  }
}

function openTopic(t) {
  editingTopic = t || null;
  $('#topicDlgTitle').textContent = t ? 'Sửa chủ đề' : 'Thêm chủ đề';
  $('#tName').value = t ? t.name : ''; $('#tDesc').value = t ? t.description : '';
  $('#topicErr').textContent = '';
  $('#topicDlg').showModal();
}
$('#addTopic').addEventListener('click', () => openTopic(null));
$('#topicCancel').addEventListener('click', () => $('#topicDlg').close());
$('#topicForm').addEventListener('submit', async e => {
  e.preventDefault();
  const body = { name: $('#tName').value, description: $('#tDesc').value };
  try {
    if (editingTopic) await api('/api/admin/topics/' + editingTopic.id, { method: 'PUT', body });
    else await api('/api/admin/topics', { method: 'POST', body });
    $('#topicDlg').close(); toast('Đã lưu chủ đề'); await loadData();
  } catch (err) {
    if (err.status === 401) { $('#topicDlg').close(); return boot(); }
    $('#topicErr').textContent = err.message;
  }
});

async function delTopic(t) {
  const extra = t.card_count ? `\n\nTất cả ${t.card_count} flashcard trong chủ đề này cũng sẽ bị xóa.` : '';
  if (!confirm(`Xóa chủ đề "${t.name}"?${extra}\n\nHành động không thể hoàn tác.`)) return;
  try { await api('/api/admin/topics/' + t.id, { method: 'DELETE' }); toast('Đã xóa chủ đề'); await loadData(); }
  catch (err) { toast(err.message); }
}

// ---------------------------------------------------------------- mật khẩu
$('#pwForm').addEventListener('submit', async e => {
  e.preventDefault();
  const err = $('#pwErr'); err.textContent = '';
  if ($('#pwNew').value !== $('#pwNew2').value) { err.textContent = 'Hai mật khẩu mới không khớp'; return; }
  try {
    await api('/api/admin/password', { method: 'POST', body: { old: $('#pwOld').value, new: $('#pwNew').value } });
    $('#pwForm').reset(); toast('Đã đổi mật khẩu');
  } catch (ex) { err.textContent = ex.message; }
});

boot();
})();
