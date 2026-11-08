"use strict";
const TOKEN = (location.hash.match(/t=([0-9a-f]+)/) || [])[1] || "";
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

const LEVEL = {
  recommended: { t: "Рекомендуем", order: 0 },
  optional: { t: "Можно", order: 1 },
  caution: { t: "Осторожно", order: 2 },
  unknown: { t: "Неизвестно", order: 3 },
  critical: { t: "Критично", order: 4 },
};

const S = {
  status: "idle", device: null, packages: [], blocked: [], phrase: "УДАЛИТЬ",
  filter: "recommended", search: "", onlyRunning: false, sort: "default",
  selected: new Set(), selectedRem: new Set(), tab: "overview",
  hist: [], statsTimer: null, stateTimer: null,
};

async function api(path, body) {
  const r = await fetch(path, {
    method: body ? "POST" : "GET",
    headers: { "X-Token": TOKEN, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  let j = {};
  try { j = await r.json(); } catch (_) {}
  if (!r.ok) { const e = new Error(j.error || ("Ошибка " + r.status)); e.status = r.status; throw e; }
  return j;
}

function toast(msg, err) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (err ? " err" : "");
  clearTimeout(toast.h);
  toast.h = setTimeout(() => t.classList.add("hidden"), 5000);
}

const fmtKB = (kb) => {
  if (!kb) return "—";
  if (kb >= 1048576) return (kb / 1048576).toFixed(1) + " ГБ";
  if (kb >= 1024) return Math.round(kb / 1024) + " МБ";
  return kb + " КБ";
};
const fmtB = (b) => (b ? fmtKB(Math.round(b / 1024)) : "—");

// ---------------- Подключение ----------------
async function pollState() {
  try {
    const s = await api("/api/state");
    const prev = S.status;
    S.status = s.status; S.device = s.device;
    if (!$("addr").value && s.lastAddress) $("addr").value = s.lastAddress.replace(/:5555$/, "");
    renderStatus(s);
    if (s.status === "connected" && prev !== "connected") onConnected();
    if (s.status !== "connected" && prev === "connected") onDisconnected(s.error);
  } catch (e) {
    renderStatus({ status: "error", error: "Программа не отвечает. Перезапустите Unbrake TV." });
  }
}

function renderStatus(s) {
  const el = $("connectStatus");
  const btns = document.querySelectorAll("#connectForm button, #btnScan, .scanitem button");
  const busy = s.status === "connecting" || s.status === "waiting_auth";
  btns.forEach((b) => (b.disabled = busy));
  if (s.status === "connecting") { el.className = "status"; el.innerHTML = '<span class="spin"></span>Подключаюсь к телевизору…'; }
  else if (s.status === "waiting_auth") { el.className = "status wait"; el.innerHTML = '<span class="spin"></span><b>Посмотрите на экран телевизора.</b><br>Отметьте «Всегда разрешать с этого компьютера» и нажмите OK пультом.'; }
  else if (s.status === "error") { el.className = "status err"; el.innerHTML = "⚠ " + esc(s.error) + hintFor(s.error); }
  else el.className = "status hidden";
}

function hintFor(err) {
  err = String(err || "");
  if (/refused/i.test(err)) return "<br><span class='small'>Телевизор не принимает подключения: включите «Отладку по USB» в разделе «Для разработчиков» и перезагрузите ТВ.</span>";
  if (/timeout|timed out|i\/o/i.test(err)) return "<br><span class='small'>Проверьте IP-адрес и что компьютер и телевизор в одной сети.</span>";
  if (/не подтвердил/.test(err)) return "<br><span class='small'>Нажмите «Подключиться» ещё раз и подтвердите запрос на телевизоре.</span>";
  return "";
}

async function connect(addr) {
  if (!addr) return toast("Введите IP-адрес телевизора", true);
  try { await api("/api/connect", { address: addr }); pollState(); }
  catch (e) { toast(e.message, true); }
}

$("connectForm").onsubmit = (e) => { e.preventDefault(); connect($("addr").value.trim()); };

$("btnScan").onclick = async () => {
  const b = $("btnScan"), box = $("scanResults");
  b.disabled = true; b.innerHTML = '<span class="spin"></span>Ищу телевизоры в сети…';
  box.innerHTML = "";
  try {
    const r = await api("/api/scan", {});
    if (!r.found || !r.found.length) {
      box.innerHTML = '<div class="status">Телевизор не найден. Проверьте, что на нём включена «Отладка по USB». Если на компьютере включён VPN или прокси — выключите его на время: он может перехватывать подключения к домашней сети. Можно ввести IP вручную (на ТВ: Настройки → Сеть → ваша сеть).</div>';
    } else {
      box.innerHTML = r.found.map((f) => `<div class="scanitem"><div><b>${esc(f.address.replace(/:5555$/, ""))}</b> <span class="muted small">${esc(f.name || "устройство с открытым ADB")}</span></div><button class="primary" data-a="${esc(f.address)}">Подключиться</button></div>`).join("");
      box.querySelectorAll("button").forEach((x) => (x.onclick = () => connect(x.dataset.a)));
    }
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = "Найти в сети";
};

function onConnected() {
  const d = S.device || {};
  $("devchip").textContent = `${d.manufacturer || ""} ${d.model || ""} · Android ${d.android || "?"}`.trim();
  $("devchip").classList.remove("hidden");
  $("btnDisconnect").classList.remove("hidden");
  $("connectView").classList.add("hidden");
  $("mainView").classList.remove("hidden");
  if (d && d.isTv === false) toast("Похоже, это не телевизор, а телефон или планшет. Будьте внимательны.", true);
  chartData.length = 0;
  switchTab("overview");
  loadPackages();
  loadTweaks();
  clearInterval(S.statsTimer);
  S.statsTimer = setInterval(loadStats, 2000);
  loadStats();
}

function onDisconnected(err) {
  clearInterval(S.statsTimer);
  $("devchip").classList.add("hidden");
  $("btnDisconnect").classList.add("hidden");
  $("mainView").classList.add("hidden");
  $("connectView").classList.remove("hidden");
  $("actionbar").classList.add("hidden");
  if (err) toast(err, true);
}

$("btnDisconnect").onclick = async () => { await api("/api/disconnect", {}); S.status = "idle"; onDisconnected(); };
$("btnQuit").onclick = async () => {
  try { await api("/api/quit", {}); } catch (_) {}
  document.body.innerHTML = '<main class="view"><div class="hero"><h1>Программа закрыта</h1><p class="muted">Можно закрыть эту вкладку.</p></div></main>';
};

// ---------------- Вкладки ----------------
document.querySelectorAll(".tabs button").forEach((b) => (b.onclick = () => switchTab(b.dataset.tab)));
function switchTab(t) {
  S.tab = t;
  document.querySelectorAll(".tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === t));
  document.querySelectorAll(".tab").forEach((x) => x.classList.toggle("hidden", x.id !== "tab-" + t));
  if (t === "history") loadHistory();
  if (t === "overview") requestAnimationFrame(drawChart);
  renderActionbar();
}

// ---------------- Нагрузка ----------------
const chartData = [];
async function loadStats() {
  if (S.status !== "connected") return;
  if (document.hidden) return;
  let s;
  try { s = await api("/api/stats"); } catch (e) { if (e.status === 409) pollState(); return; }
  const memUsed = s.memTotalKb ? 100 * (1 - s.memAvailKb / s.memTotalKb) : 0;
  const stor = s.storTotalKb ? (100 * s.storUsedKb) / s.storTotalKb : 0;
  if (chartData.length || s.cpu > 0) chartData.push({ cpu: s.cpu, mem: memUsed });
  if (chartData.length > 60) chartData.shift();

  setMetric("Cpu", s.cpu, s.cpu.toFixed(0) + "%", 60, 85);
  setMetric("Mem", memUsed, memUsed.toFixed(0) + "%", 75, 90);
  $("sMem").textContent = `свободно ${fmtKB(s.memAvailKb)} из ${fmtKB(s.memTotalKb)}`;
  setMetric("Stor", stor, stor.toFixed(0) + "%", 80, 92);
  $("sStor").textContent = s.storTotalKb ? `свободно ${fmtKB(s.storTotalKb - s.storUsedKb)} из ${fmtKB(s.storTotalKb)}` : "";
  $("mTemp").textContent = s.tempC ? s.tempC.toFixed(0) + " °C" : "—";
  $("sTemp").textContent = s.tempC ? (s.tempC > 75 ? "Горячо — проверьте вентиляцию" : "В норме") : "датчик недоступен";

  $("topBody").innerHTML = (s.top || []).map((p) => {
    const known = findPkg(p.package);
    const lv = (known || p).level || "unknown";
    const name = (known || {}).name || p.name || p.package;
    return `<tr><td><div class="appname">${esc(name)}</div><div class="pkg">${esc(p.package)}</div></td>
      <td><span class="badge ${lv}">${LEVEL[lv].t}</span></td>
      <td class="num">${p.cpu.toFixed(1)}%</td><td class="num">${fmtKB(p.memKb)}</td></tr>`;
  }).join("") || '<tr><td colspan="4" class="muted">Нет данных о процессах</td></tr>';

  renderAdvice(s, memUsed, stor);
  if (S.tab === "overview") drawChart();
}

// Process names like "com.google.android.gms.persistent" belong to the longest matching package.
function findPkg(proc) {
  let best = null;
  for (const p of S.packages) {
    if ((proc === p.id || proc.startsWith(p.id + ".")) && (!best || p.id.length > best.id.length)) best = p;
  }
  return best;
}
const plural = (n, one, few, many) => {
  const a = n % 10, b = n % 100;
  return a === 1 && b !== 11 ? one : a >= 2 && a <= 4 && (b < 12 || b > 14) ? few : many;
};

function setMetric(k, v, text, warn, bad) {
  $("m" + k).textContent = text;
  const b = $("b" + k);
  b.style.width = Math.min(100, v) + "%";
  b.className = v >= bad ? "bad" : v >= warn ? "warn" : "";
}

function renderAdvice(s, mem, stor) {
  const rec = S.packages.filter((p) => p.level === "recommended" && p.state === "installed");
  const recRunning = rec.filter((p) => p.running);
  const recMem = recRunning.reduce((a, p) => a + (p.memKb || 0), 0);
  const tips = [];
  if (recRunning.length) tips.push(`Сейчас в фоне работает <b>${recRunning.length}</b> ${plural(recRunning.length, "ненужное приложение", "ненужных приложения", "ненужных приложений")} из списка «Рекомендуем удалить» — они занимают около <b>${fmtKB(recMem)}</b> памяти.`);
  if (mem > 80) tips.push("Оперативная память почти заполнена — это главная причина тормозов. Удаление фоновых приложений поможет сильнее всего.");
  if (stor > 85) tips.push("Внутренняя память почти заполнена — система будет тормозить. Удалите неиспользуемые приложения или очистите их кэш.");
  if (rec.length && !recRunning.length) tips.push(`Найдено <b>${rec.length}</b> ${plural(rec.length, "приложение", "приложения", "приложений")}, которые безопасно удалить.`);
  const el = $("advice");
  if (!tips.length) { el.classList.add("hidden"); return; }
  el.classList.remove("hidden");
  el.innerHTML = `<h2>Что можно улучшить</h2><ul>${tips.map((t) => "<li>" + t + "</li>").join("")}</ul>
    ${rec.length ? '<button class="primary" id="goRec">Показать, что удалить</button>' : ""}`;
  const g = $("goRec");
  if (g) g.onclick = () => { setFilter("recommended"); switchTab("apps"); };
}

function drawChart() {
  const c = $("chart");
  const dpr = window.devicePixelRatio || 1;
  const w = c.clientWidth, h = 180;
  if (!w) return;
  c.width = w * dpr; c.height = h * dpr;
  const g = c.getContext("2d");
  g.scale(dpr, dpr);
  g.clearRect(0, 0, w, h);
  const css = getComputedStyle(document.documentElement);
  const pad = { l: 34, r: 8, t: 8, b: 18 };
  g.strokeStyle = css.getPropertyValue("--line"); g.fillStyle = css.getPropertyValue("--muted");
  g.font = "11px Segoe UI, sans-serif"; g.lineWidth = 1;
  for (const v of [0, 50, 100]) {
    const y = pad.t + (h - pad.t - pad.b) * (1 - v / 100);
    g.beginPath(); g.moveTo(pad.l, y); g.lineTo(w - pad.r, y); g.stroke();
    g.fillText(v + "%", 2, y + 4);
  }
  const line = (key, color) => {
    if (chartData.length < 2) return;
    g.strokeStyle = color; g.lineWidth = 2; g.beginPath();
    chartData.forEach((p, i) => {
      const x = pad.l + ((w - pad.l - pad.r) * (i + 60 - chartData.length)) / 59;
      const y = pad.t + (h - pad.t - pad.b) * (1 - Math.min(100, p[key]) / 100);
      i ? g.lineTo(x, y) : g.moveTo(x, y);
    });
    g.stroke();
  };
  line("mem", css.getPropertyValue("--unk"));
  line("cpu", css.getPropertyValue("--accent"));
}
window.addEventListener("resize", () => S.tab === "overview" && drawChart());

// ---------------- Приложения ----------------
async function loadPackages() {
  try {
    const r = await api("/api/packages");
    S.packages = r.packages || []; S.blocked = r.blocked || []; S.phrase = r.confirmPhrase || "УДАЛИТЬ";
    const ids = new Set(S.packages.map((p) => p.id));
    [...S.selected].forEach((id) => { const p = S.packages.find((x) => x.id === id); if (!p || p.state !== "installed") S.selected.delete(id); });
    [...S.selectedRem].forEach((id) => { const p = S.packages.find((x) => x.id === id); if (!p || p.state === "installed") S.selectedRem.delete(id); });
    renderApps(); renderRemoved();
  } catch (e) { toast("Не удалось получить список приложений: " + e.message, true); }
}

function setFilter(f) {
  S.filter = f;
  document.querySelectorAll("#filters button").forEach((b) => b.classList.toggle("active", b.dataset.f === f));
  renderApps();
}
document.querySelectorAll("#filters button").forEach((b) => (b.onclick = () => setFilter(b.dataset.f)));
$("search").oninput = (e) => { S.search = e.target.value.toLowerCase(); renderApps(); };
$("onlyRunning").onchange = (e) => { S.onlyRunning = e.target.checked; renderApps(); };
document.querySelectorAll("th[data-sort]").forEach((th) => (th.onclick = () => { S.sort = S.sort === th.dataset.sort ? "default" : th.dataset.sort; renderApps(); }));

function visibleApps() {
  let list = S.packages.filter((p) => p.state === "installed");
  if (S.filter !== "all") list = list.filter((p) => p.level === S.filter);
  if (S.search) list = list.filter((p) => (p.name + " " + p.id).toLowerCase().includes(S.search));
  if (S.onlyRunning) list = list.filter((p) => p.running);
  const sorters = {
    name: (a, b) => a.name.localeCompare(b.name, "ru"),
    mem: (a, b) => (b.memKb || 0) - (a.memKb || 0),
    size: (a, b) => (b.sizeBytes || 0) - (a.sizeBytes || 0),
    default: (a, b) => LEVEL[a.level].order - LEVEL[b.level].order || (b.memKb || 0) - (a.memKb || 0) || a.name.localeCompare(b.name, "ru"),
  };
  return list.sort(sorters[S.sort] || sorters.default);
}

function tagsFor(p) {
  const t = [];
  if (p.running) t.push('<span class="tag run">запущено</span>');
  if (p.isDefaultLauncher) t.push('<span class="tag">текущий рабочий стол</span>');
  else if (p.isLauncher) t.push('<span class="tag">рабочий стол</span>');
  if (p.isKeyboard) t.push('<span class="tag">клавиатура</span>');
  if (!p.system) t.push('<span class="tag">установлено пользователем</span>');
  if (p.state === "disabled") t.push('<span class="tag">отключено</span>');
  return t.length ? `<div class="tags">${t.join("")}</div>` : "";
}

function renderApps() {
  const list = visibleApps();
  const rec = S.packages.filter((p) => p.level === "recommended" && p.state === "installed").length;
  $("recCount").textContent = rec; $("recCount").classList.toggle("hidden", !rec);
  $("appsBody").innerHTML = list.map((p) => {
    const locked = S.blocked.includes(p.id);
    const sel = S.selected.has(p.id);
    return `<tr class="${sel ? "sel" : ""} ${locked ? "locked" : ""}">
      <td class="cb"><input type="checkbox" data-id="${esc(p.id)}" ${sel ? "checked" : ""} ${locked ? "disabled title='Удаление запрещено'" : ""}></td>
      <td><div class="appname">${esc(p.name)}</div><div class="pkg">${esc(p.id)}</div><div class="reason">${esc(p.reason)}</div>${tagsFor(p)}</td>
      <td><span class="badge ${p.level}">${LEVEL[p.level].t}</span></td>
      <td class="num">${p.running ? fmtKB(p.memKb) : "—"}</td>
      <td class="num">${fmtB(p.sizeBytes)}</td></tr>`;
  }).join("");
  $("appsEmpty").classList.toggle("hidden", list.length > 0);
  $("appsBody").querySelectorAll("input[type=checkbox]").forEach((cb) => (cb.onchange = () => {
    cb.checked ? S.selected.add(cb.dataset.id) : S.selected.delete(cb.dataset.id);
    cb.closest("tr").classList.toggle("sel", cb.checked);
    renderActionbar();
  }));
  const selectable = list.filter((p) => !S.blocked.includes(p.id));
  $("selAll").checked = selectable.length > 0 && selectable.every((p) => S.selected.has(p.id));
  renderActionbar();
}

$("selAll").onchange = (e) => {
  visibleApps().filter((p) => !S.blocked.includes(p.id)).forEach((p) => (e.target.checked ? S.selected.add(p.id) : S.selected.delete(p.id)));
  renderApps();
};

// ---------------- Удалённые ----------------
function renderRemoved() {
  const list = S.packages.filter((p) => p.state !== "installed").sort((a, b) => a.name.localeCompare(b.name, "ru"));
  $("remCount").textContent = list.length; $("remCount").classList.toggle("hidden", !list.length);
  $("remBody").innerHTML = list.map((p) => `<tr class="${S.selectedRem.has(p.id) ? "sel" : ""}">
    <td class="cb"><input type="checkbox" data-id="${esc(p.id)}" ${S.selectedRem.has(p.id) ? "checked" : ""}></td>
    <td><div class="appname">${esc(p.name)}</div><div class="pkg">${esc(p.id)}</div></td>
    <td><span class="badge ${p.level}">${LEVEL[p.level].t}</span></td>
    <td>${p.state === "removed" ? "удалено" : "отключено"}</td></tr>`).join("");
  $("remEmpty").classList.toggle("hidden", list.length > 0);
  $("remBody").querySelectorAll("input").forEach((cb) => (cb.onchange = () => {
    cb.checked ? S.selectedRem.add(cb.dataset.id) : S.selectedRem.delete(cb.dataset.id);
    cb.closest("tr").classList.toggle("sel", cb.checked);
    renderActionbar();
  }));
  $("selAllRem").checked = list.length > 0 && list.every((p) => S.selectedRem.has(p.id));
  renderActionbar();
}
$("selAllRem").onchange = (e) => {
  S.packages.filter((p) => p.state !== "installed").forEach((p) => (e.target.checked ? S.selectedRem.add(p.id) : S.selectedRem.delete(p.id)));
  renderRemoved();
};

// ---------------- Панель действий ----------------
function renderActionbar() {
  const bar = $("actionbar");
  if (S.tab === "apps" && S.selected.size) {
    const pk = [...S.selected].map((id) => S.packages.find((p) => p.id === id)).filter(Boolean);
    const danger = pk.filter((p) => p.level === "critical").length;
    $("selInfo").innerHTML = `Выбрано: <b>${pk.length}</b>` + (danger ? ` · <span style="color:#fca5a5">критичных: ${danger}</span>` : "");
    $("btnAction").textContent = "Удалить выбранные";
    $("btnAction").className = "danger";
    bar.classList.remove("hidden");
  } else if (S.tab === "removed" && S.selectedRem.size) {
    $("selInfo").innerHTML = `Выбрано: <b>${S.selectedRem.size}</b>`;
    $("btnAction").textContent = "Вернуть выбранные";
    $("btnAction").className = "primary";
    bar.classList.remove("hidden");
  } else bar.classList.add("hidden");
}
$("btnClear").onclick = () => {
  if (S.tab === "apps") { S.selected.clear(); renderApps(); } else { S.selectedRem.clear(); renderRemoved(); }
};
$("btnAction").onclick = () => (S.tab === "apps" ? confirmRemove() : doRestore());

// ---------------- Подтверждение (защита от дурака) ----------------
function modal({ title, body, ok, okClass, onOpen }) {
  return new Promise((resolve) => {
    $("mTitle").textContent = title;
    $("mBody").innerHTML = body;
    $("mOk").textContent = ok || "OK";
    $("mOk").className = okClass || "danger";
    $("modal").classList.remove("hidden");
    const close = (v) => { $("modal").classList.add("hidden"); $("mOk").onclick = $("mCancel").onclick = null; resolve(v); };
    $("mCancel").onclick = () => close(false);
    $("mOk").onclick = () => {
      const m = document.querySelector('input[name="method"]:checked');
      S.lastMethod = m ? m.value : "uninstall";
      close(true);
    };
    onOpen && onOpen();
  });
}

function groupHtml(level, items, note) {
  if (!items.length) return "";
  return `<div class="group ${level}"><span class="badge ${level}">${LEVEL[level].t}</span> ${note}
    <ul>${items.map((p) => `<li><b>${esc(p.name)}</b> <span class="pkg">${esc(p.id)}</span>${level === "critical" || level === "caution" ? `<div class="reason">${esc(p.reason)}</div>` : ""}</li>`).join("")}</ul></div>`;
}

async function confirmRemove() {
  const pk = [...S.selected].map((id) => S.packages.find((p) => p.id === id)).filter(Boolean);
  const by = (l) => pk.filter((p) => p.level === l);
  const crit = by("critical"), warn = [...by("caution"), ...by("unknown")];
  const user = pk.filter((p) => !p.system);
  const launchers = S.packages.filter((p) => p.isLauncher && p.state === "installed");
  const noLauncher = launchers.length > 0 && launchers.every((p) => S.selected.has(p.id));
  const needPhrase = crit.length > 0 || noLauncher;

  let body = `<p>Будет удалено приложений: <b>${pk.length}</b>. Системные приложения удаляются только для пользователя — их можно вернуть на вкладке «Удалённые».</p>`;
  if (noLauncher) body += `<div class="group critical"><b>⚠ Вы удаляете все рабочие столы.</b> После нажатия «Домой» будет пустой экран. Сначала установите другой лаунчер (например, Projectivy Launcher из Play Маркета).</div>`;
  body += groupHtml("critical", crit, "— <b>может сломать телевизор</b>: бесконечная загрузка, нет изображения или настроек. Исправить сможет только сброс к заводским настройкам.");
  body += groupHtml("caution", by("caution"), "— телевизор продолжит работать, но пропадут функции:");
  body += groupHtml("unknown", by("unknown"), "— о назначении этих компонентов ничего не известно:");
  body += groupHtml("optional", by("optional"), "");
  body += groupHtml("recommended", by("recommended"), "");
  if (user.length) body += `<p class="small muted">Приложения, установленные вами (${user.length}), удалятся полностью — вернуть их можно будет только из Play Маркета.</p>`;
  body += `<div class="method"><b>Способ</b>
    <label><input type="radio" name="method" value="uninstall" checked><span><b>Удалить для пользователя</b><br><span class="small muted">Освобождает память устройства и оперативную память. Вернуть можно на вкладке «Удалённые».</span></span></label>
    <label><input type="radio" name="method" value="disable"><span><b>Только отключить</b><br><span class="small muted">Самый осторожный вариант: приложение остаётся на месте, но не запускается. Освобождает оперативную память.</span></span></label></div>`;
  if (warn.length && !needPhrase) body += `<label class="chk confirmbox"><input type="checkbox" id="ackWarn"> Я понимаю, что эти функции перестанут работать</label>`;
  if (needPhrase) body += `<div class="confirmbox"><label class="chk"><input type="checkbox" id="ackWarn"> Я понимаю риск и знаю, как сделать сброс к заводским настройкам</label>
    <div style="margin-top:10px">Для подтверждения введите слово <b>${esc(S.phrase)}</b>:</div><input id="phrase" autocomplete="off" spellcheck="false"></div>`;

  const ok = await modal({
    title: needPhrase ? "⚠ Опасное удаление" : "Удалить выбранные приложения?",
    body, ok: "Удалить",
    onOpen: () => {
      const btn = $("mOk");
      const check = () => {
        const ack = $("ackWarn") ? $("ackWarn").checked : true;
        const ph = $("phrase") ? $("phrase").value.trim().toUpperCase() === S.phrase : true;
        btn.disabled = !(ack && ph);
      };
      ["ackWarn", "phrase"].forEach((id) => $(id) && ($(id).oninput = $(id).onchange = check));
      check();
    },
  });
  const method = S.lastMethod || "uninstall";
  if (!ok) return;
  await runChange("/api/remove", { packages: pk.map((p) => p.id), accepted: true, confirm: needPhrase ? S.phrase : "", method }, "Удаление");
  S.selected.clear();
}

async function doRestore() {
  const ids = [...S.selectedRem];
  await runChange("/api/restore", { packages: ids }, "Восстановление");
  S.selectedRem.clear();
}

async function runChange(path, body, title) {
  $("btnAction").disabled = true;
  $("selInfo").innerHTML = '<span class="spin"></span>Выполняю…';
  try {
    const r = await api(path, body);
    const res = r.results || [];
    const okN = res.filter((x) => x.ok).length;
    const html = `<p>Успешно: <b>${okN}</b> из ${res.length}</p>` + res.map((x) => {
      const p = S.packages.find((q) => q.id === x.id) || { name: x.id };
      const how = x.method !== "disable" ? "" : body.method === "disable" ? " (отключено)" : " (отключено — удалить не разрешено, но работать оно не будет)";
      return `<div class="res"><span class="${x.ok ? "ok" : "bad"}">${x.ok ? "✓" : "✗"}</span><div><b>${esc(p.name)}</b>${how}<div class="pkg">${esc(x.id)}${x.ok ? "" : " — " + esc(x.output)}</div></div></div>`;
    }).join("") + (okN && path.includes("remove") ? '<p class="small muted">Перезагрузите телевизор, чтобы освободить память.</p>' : "");
    await modal({ title: title + " завершено", body: html, ok: "Готово", okClass: "primary" });
    $("mCancel").classList.remove("hidden");
  } catch (e) {
    toast(e.message, true);
  }
  $("btnAction").disabled = false;
  await loadPackages();
}

// ---------------- Журнал ----------------
async function loadHistory() {
  try {
    const r = await api("/api/history");
    const h = r.history || [];
    $("historyList").innerHTML = h.length ? h.map((e) => {
      const ok = e.results.filter((x) => x.ok).length;
      return `<details class="card hist"><summary><b>${{ remove: "Удаление", restore: "Восстановление", tweak: "Улучшение" }[e.action] || esc(e.action)}</b> · ${esc(e.time)} · ${esc(e.device)} · успешно ${ok} из ${e.results.length}</summary>
        ${e.results.map((x) => `<div class="res"><span class="${x.ok ? "ok" : "bad"}">${x.ok ? "✓" : "✗"}</span><span class="pkg">${esc(x.method === "tweak" ? x.output : x.id + (x.method === "disable" ? " (отключено)" : ""))}</span></div>`).join("")}</details>`;
    }).join("") : '<div class="empty">Журнал пуст</div>';
  } catch (e) { toast(e.message, true); }
}

// ---------------- Быстрые улучшения ----------------
async function loadTweaks() {
  try {
    const t = await api("/api/tweaks");
    document.querySelectorAll("#animSeg button").forEach((b) => b.classList.toggle("active", b.dataset.v === t.animScale));
  } catch (_) {}
}
document.querySelectorAll("#animSeg button").forEach((b) => (b.onclick = async () => {
  try { await api("/api/tweak", { action: b.dataset.a }); toast("Скорость анимаций изменена"); loadTweaks(); }
  catch (e) { toast(e.message, true); }
}));
const TWEAK_CONFIRM = {
  reboot: { title: "Перезагрузить телевизор?", body: "<p>Программа отключится от телевизора. Подключитесь заново через минуту, когда он загрузится.</p>", ok: "Перезагрузить" },
  adb_off: { title: "Выключить отладку?", body: "<p>Телевизор перестанет принимать подключения по сети, и программа отключится. Чтобы подключиться снова, включите «Отладку по USB» в разделе «Для разработчиков».</p>", ok: "Выключить" },
};
document.querySelectorAll("[data-tweak]").forEach((b) => (b.onclick = async () => {
  const a = b.dataset.tweak, c = TWEAK_CONFIRM[a];
  if (c && !(await modal({ ...c, okClass: "primary" }))) return;
  const old = b.textContent;
  b.disabled = true; b.innerHTML = '<span class="spin"></span>Подождите…';
  try {
    const r = await api("/api/tweak", { action: a });
    toast(r.message);
    if (c) { S.status = "idle"; onDisconnected(); }
    else if (a === "trim_caches") loadPackages();
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = old;
}));

// ---------------- Профили ----------------
$("btnExport").onclick = () => {
  const removed = S.packages.filter((p) => p.state !== "installed").map((p) => p.id).sort();
  if (!removed.length) return toast("Пока нечего сохранять — на телевизоре ничего не удалено", true);
  const d = S.device || {};
  const profile = { app: "Unbrake TV", format: 1, created: new Date().toISOString(), device: { manufacturer: d.manufacturer, model: d.model, android: d.android }, packages: removed };
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([JSON.stringify(profile, null, 2)], { type: "application/json" }));
  a.download = `unbrake-${(d.manufacturer + "-" + d.model).replace(/[^\w.-]+/g, "_")}.json`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  toast(`Профиль сохранён: ${removed.length} ${plural(removed.length, "приложение", "приложения", "приложений")}`);
};
$("btnImport").onclick = () => $("importFile").click();
$("importFile").onchange = async (e) => {
  const f = e.target.files[0];
  e.target.value = "";
  if (!f) return;
  let prof;
  try { prof = JSON.parse(await f.text()); } catch (_) { return toast("Это не файл профиля Unbrake TV", true); }
  const ids = Array.isArray(prof.packages) ? prof.packages.filter((x) => typeof x === "string") : [];
  if (!ids.length) return toast("В профиле нет приложений", true);
  let n = 0;
  ids.forEach((id) => {
    const p = S.packages.find((x) => x.id === id && x.state === "installed");
    if (p && !S.blocked.includes(id)) { S.selected.add(id); n++; }
  });
  setFilter("all");
  toast(n ? `Отмечено ${n} из ${ids.length}. Проверьте список и нажмите «Удалить выбранные».` : "Все приложения из профиля уже удалены или отсутствуют на этом ТВ");
};

// ---------------- Старт ----------------
if (!TOKEN) document.body.innerHTML = '<main class="view"><h1>Откройте программу Unbrake TV заново</h1><p class="muted">Эта страница открыта без ключа доступа.</p></main>';
else { pollState(); S.stateTimer = setInterval(pollState, 1500); }
