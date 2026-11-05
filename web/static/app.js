(() => {
'use strict';

const $ = (s, r = document) => r.querySelector(s);
const DAY = 86400000;
// Ôn tập ngắt quãng (Leitner): 10 phút, 1, 3, 7, 14, 30 ngày
const INTERVALS = [10 * 60000, DAY, 3 * DAY, 7 * DAY, 14 * DAY, 30 * DAY];

const norm = s => (s || '').toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '').replace(/đ/g, 'd');
const load = (k, d) => { try { const v = JSON.parse(localStorage.getItem(k)); return v ?? d; } catch { return d; } };
const save = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* bộ nhớ đầy hoặc bị chặn */ } };
function el(tag, cls, text) { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.hidden = false;
  clearTimeout(toast.t); toast.t = setTimeout(() => { t.hidden = true; }, 1800);
}
function shuffleArr(a) { for (let i = a.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [a[i], a[j]] = [a[j], a[i]]; } return a; }

let progress = load('fc_progress', {});   // { [cardId]: {fav, hard, seen, box, due, reps, last} }
let history = load('fc_history', []);     // [{t, c, tp, r}]
let topics = [], cards = [];
let cur = { topic: null, q: '', mode: 'all', shuffle: false };
let deck = [], idx = 0, flipped = false;

const isDue = c => { const p = progress[c.id]; return !p || !p.seen || (p.due || 0) <= Date.now(); };

// ---------------------------------------------------------------- dữ liệu
async function init() {
  try {
    const [t, c] = await Promise.all([
      fetch('/api/topics').then(r => r.json()),
      fetch('/api/cards').then(r => r.json()),
    ]);
    topics = t; cards = c;
    // bỏ tiến độ của thẻ đã bị xóa
    const ids = new Set(cards.map(x => String(x.id)));
    let changed = false;
    for (const k of Object.keys(progress)) if (!ids.has(k)) { delete progress[k]; changed = true; }
    if (changed) save('fc_progress', progress);
  } catch {
    toast('Không tải được dữ liệu. Kiểm tra kết nối mạng.');
  }
  renderHome();
}

// ---------------------------------------------------------------- trang chủ
function renderHome() {
  const grid = $('#topicGrid');
  grid.replaceChildren();
  const tiles = [{ id: null, name: 'Tất cả chủ đề', description: 'Học mọi flashcard', list: cards, all: true }];
  for (const t of topics) tiles.push({ id: t.id, name: t.name, description: t.description, list: cards.filter(c => c.topic_id === t.id) });

  for (const t of tiles) {
    const total = t.list.length;
    const learned = t.list.filter(c => progress[c.id]?.seen).length;
    const due = t.list.filter(isDue).length;
    const b = el('button', 'tile' + (t.all ? ' all' : ''));
    b.type = 'button';
    const bar = el('div', 'bar'); const fill = el('i'); fill.style.width = total ? (learned / total * 100) + '%' : '0%'; bar.append(fill);
    b.append(el('h3', '', t.name), el('p', 'muted small', t.description || ''), bar,
      el('p', 'small', `${total} thẻ · đã học ${learned} · cần ôn ${due}`));
    b.addEventListener('click', () => startStudy(t.id, ''));
    grid.append(b);
  }
  const t0 = new Date(); t0.setHours(0, 0, 0, 0);
  $('#statToday').textContent = `Hôm nay: ${history.filter(h => h.t >= t0.getTime()).length} lượt ôn`;
  $('#statWeek').textContent = `7 ngày qua: ${history.filter(h => h.t >= Date.now() - 7 * DAY).length} lượt ôn`;
}

$('#homeSearch').addEventListener('submit', e => { e.preventDefault(); startStudy(null, $('#homeQ').value); });
$('#resetProgress').addEventListener('click', () => {
  if (!confirm('Xóa toàn bộ tiến độ, yêu thích và lịch sử học trên thiết bị này?')) return;
  progress = {}; history = []; save('fc_progress', progress); save('fc_history', history);
  renderHome(); toast('Đã xóa tiến độ');
});

// ---------------------------------------------------------------- học
function startStudy(topicId, q) {
  cur = { topic: topicId, q: q || '', mode: 'all', shuffle: false };
  $('#q').value = cur.q; $('#mode').value = 'all';
  $('#shuffle').setAttribute('aria-pressed', 'false');
  const t = topics.find(x => x.id === topicId);
  $('#studyTitle').textContent = t ? t.name : 'Tất cả chủ đề';
  $('#home').hidden = true; $('#study').hidden = false;
  window.scrollTo(0, 0);
  rebuild();
}

function rebuild() {
  const q = norm(cur.q.trim());
  let list = cards.filter(c => cur.topic == null || c.topic_id === cur.topic);
  if (q) list = list.filter(c => norm(`${c.back} ${c.keywords} ${c.topic_name}`).includes(q));
  list = list.filter(c => {
    const p = progress[c.id] || {};
    switch (cur.mode) {
      case 'new': return !p.seen;
      case 'due': return isDue(c);
      case 'fav': return !!p.fav;
      case 'hard': return !!p.hard;
      default: return true;
    }
  });
  if (cur.shuffle) shuffleArr(list);
  deck = list; idx = 0;
  show();
}

function setFlipped(v, animate = true) {
  const card = $('#card');
  if (!animate) card.classList.add('noanim');
  card.classList.toggle('flipped', v);
  if (!animate) { void card.offsetWidth; card.classList.remove('noanim'); }
  flipped = v;
  $('#rate').hidden = !v;
}

function show() {
  const has = deck.length > 0;
  $('#player').hidden = !has;
  $('#empty').hidden = has;
  if (!has) {
    const msgs = { due: 'Bạn đã ôn hết thẻ đến hạn. Quay lại sau nhé.', new: 'Bạn đã học hết các thẻ trong mục này.', fav: 'Chưa có thẻ yêu thích nào.', hard: 'Chưa có thẻ nào đánh dấu khó nhớ.' };
    $('#emptyMsg').textContent = cur.q ? 'Không tìm thấy thẻ khớp từ khóa.' : (msgs[cur.mode] || 'Chủ đề này chưa có flashcard.');
    return;
  }
  const c = deck[idx];
  setFlipped(false, false);                 // luôn bắt đầu ở mặt trước, không hiện hiệu ứng lật
  $('#frontImg').src = c.image;
  $('#backText').textContent = c.back;
  $('#backKeys').textContent = c.keywords || '';
  $('#counter').textContent = `${idx + 1} / ${deck.length}`;
  $('#topicBadge').textContent = c.topic_name;
  updateMarks();
  const n = deck[(idx + 1) % deck.length]; if (n) { new Image().src = n.image; }
}

function updateMarks() {
  const p = progress[deck[idx].id] || {};
  $('#favBtn').setAttribute('aria-pressed', String(!!p.fav));
  $('#hardBtn').setAttribute('aria-pressed', String(!!p.hard));
}

function go(d) {
  if (!deck.length) return;
  idx = (idx + d + deck.length) % deck.length;
  show();
}

function flip() { if (deck.length) setFlipped(!flipped); }

function mark(k) {
  const c = deck[idx]; const p = progress[c.id] || (progress[c.id] = {});
  p[k] = !p[k];
  save('fc_progress', progress); updateMarks();
}

function rate(r) {
  const c = deck[idx]; const p = progress[c.id] || (progress[c.id] = {});
  let box = p.box ?? 0;
  box = r === 'again' ? 0 : Math.min(5, box + (r === 'easy' ? 2 : 1));
  p.box = box; p.seen = true; p.reps = (p.reps || 0) + 1; p.last = Date.now();
  p.due = Date.now() + INTERVALS[box];
  history.push({ t: Date.now(), c: c.id, tp: c.topic_id, r });
  if (history.length > 2000) history = history.slice(-2000);
  save('fc_progress', progress); save('fc_history', history);
  if (idx < deck.length - 1) go(1); else { setFlipped(false); toast('Đã hết bộ thẻ'); }
}

// ---------------------------------------------------------------- sự kiện
$('#card').addEventListener('click', flip);
$('#prev').addEventListener('click', () => go(-1));
$('#next').addEventListener('click', () => go(1));
$('#favBtn').addEventListener('click', () => mark('fav'));
$('#hardBtn').addEventListener('click', () => mark('hard'));
$('#rate').addEventListener('click', e => { const b = e.target.closest('[data-r]'); if (b) rate(b.dataset.r); });
$('#shuffle').addEventListener('click', () => {
  cur.shuffle = !cur.shuffle;
  $('#shuffle').setAttribute('aria-pressed', String(cur.shuffle));
  rebuild();
});
$('#mode').addEventListener('change', e => { cur.mode = e.target.value; rebuild(); });
let qTimer;
$('#q').addEventListener('input', e => { clearTimeout(qTimer); qTimer = setTimeout(() => { cur.q = e.target.value; rebuild(); }, 200); });
$('#backHome').addEventListener('click', () => { $('#study').hidden = true; $('#home').hidden = false; renderHome(); });

document.addEventListener('keydown', e => {
  if ($('#study').hidden || /^(INPUT|SELECT|TEXTAREA)$/.test(e.target.tagName)) return;
  if (e.key === 'ArrowRight') go(1);
  else if (e.key === 'ArrowLeft') go(-1);
  else if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); flip(); }
});

// vuốt trái/phải để chuyển thẻ
let sx = 0, sy = 0;
const scene = $('#scene');
scene.addEventListener('touchstart', e => { sx = e.touches[0].clientX; sy = e.touches[0].clientY; }, { passive: true });
scene.addEventListener('touchend', e => {
  const dx = e.changedTouches[0].clientX - sx, dy = e.changedTouches[0].clientY - sy;
  if (Math.abs(dx) > 70 && Math.abs(dy) < 50) go(dx < 0 ? 1 : -1);
}, { passive: true });

init();
})();
