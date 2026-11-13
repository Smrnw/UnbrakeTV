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
const lvl = (l) => (LEVEL[l] ? l : "unknown");

const S = {
  status: "idle", device: null, demo: false, packages: [], blocked: [], phrase: "УДАЛИТЬ",
  filter: "recommended", search: "", onlyRunning: false, sort: "default",
  selected: new Set(), selectedRem: new Set(), tab: "overview",
  statsTimer: null, stateTimer: null, statsBusy: false, version: "",
  launchers: null, updateDismissed: false, pairPrefill: "",
};

async function api(path, body, raw) {
  const opt = { method: body ? "POST" : "GET", headers: { "X-Token": TOKEN } };
  if (raw) { opt.body = body; Object.assign(opt.headers, raw); }
  else if (body) { opt.body = JSON.stringify(body); opt.headers["Content-Type"] = "application/json"; }
  const r = await fetch(path, opt);
  let j = {};
  try { j = await r.json(); } catch (_) {}
  if (!r.ok) { const e = new Error(j.error || ("Ошибка " + r.status)); e.status = r.status; throw e; }
  return j;
}

// Server messages start with a lower-case letter; on screen they are sentences.
const cap = (s) => { s = String(s || ""); return s.charAt(0).toUpperCase() + s.slice(1); };

function toast(msg, err) {
  const t = $("toast");
  t.textContent = cap(msg);
  t.className = "toast" + (err ? " err" : "");
  clearTimeout(toast.h);
  toast.h = setTimeout(() => t.classList.add("hidden"), err ? 8000 : 5000);
}

const fmtKB = (kb) => {
  if (!kb) return "—";
  if (kb >= 1048576) return (kb / 1048576).toFixed(1) + " ГБ";
  if (kb >= 1024) return Math.round(kb / 1024) + " МБ";
  return kb + " КБ";
};
const fmtB = (b) => (b ? fmtKB(Math.round(b / 1024)) : "—");
const plural = (n, one, few, many) => {
  const a = n % 10, b = n % 100;
  return a === 1 && b !== 11 ? one : a >= 2 && a <= 4 && (b < 12 || b > 14) ? few : many;
};
const apps = (n) => `${n} ${plural(n, "приложение", "приложения", "приложений")}`;

function download(name, text) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}
const fileTag = () => { const d = S.device || {}; return `${d.manufacturer || "tv"}-${d.model || ""}`.replace(/[^\w.-]+/g, "_"); };

// ---------------- Подключение ----------------
async function pollState() {
  let s;
  try { s = await api("/api/state"); }
  catch (e) { renderStatus({ status: "error", error: "Программа не отвечает. Перезапустите Unbrake TV." }); return; }
  const prev = S.status;
  S.status = s.status; S.device = s.device; S.demo = !!s.demo; S.version = s.version;
  if (!$("addr").value && s.lastAddress && document.activeElement !== $("addr")) $("addr").value = s.lastAddress.replace(/:5555$/, "");
  renderStatus(s);
  renderRecent(s.recent || []);
  renderUpdate(s);
  renderProgress(s);
  if (s.status === "connected" && prev !== "connected") onConnected();
  if (s.status !== "connected" && prev === "connected") onDisconnected(s.status === "error" ? s.error : "");
}

function setHtml(el, html) { if (el._html !== html) { el._html = html; el.innerHTML = html; } }

function renderStatus(s) {
  const el = $("connectStatus");
  const busy = s.status === "connecting" || s.status === "waiting_auth";
  document.querySelectorAll("#connectForm button, #btnScan, .scanitem button, #recentBox button, #btnPair, #btnDemo").forEach((b) => (b.disabled = busy));
  let cls = "status", html = "";
  if (s.status === "connecting") html = '<span class="spin"></span>Подключаюсь к телевизору…';
  else if (s.status === "waiting_auth") { cls = "status wait"; html = '<span class="spin"></span><b>Посмотрите на экран телевизора.</b><br>Отметьте «Всегда разрешать с этого компьютера» и нажмите OK пультом.'; }
  else if (s.status === "rebooting") { cls = "status wait"; html = '<span class="spin"></span><b>Телевизор перезагружается.</b><br>Подключусь автоматически, как только он включится — обычно это занимает около минуты.<br><button class="ghost inbtn" data-act="cancel">Не ждать</button>'; }
  else if (s.status === "error") {
    cls = "status err";
    html = "⚠ " + esc(cap(s.error)) + hintFor(s.error) + (s.needsPairing ? '<br><button class="primary inbtn" data-act="pair">Сопряжение по коду</button>' : "");
  } else cls = "status hidden";
  el.className = cls;
  setHtml(el, html);
}
$("connectStatus").onclick = async (e) => {
  const act = e.target.dataset && e.target.dataset.act;
  if (act === "pair") pairDialog();
  if (act === "cancel") { try { await api("/api/disconnect", {}); } catch (_) {} pollState(); }
};

function hintFor(err) {
  err = String(err || "");
  if (/refused/i.test(err)) return "<br><span class='small'>Телевизор не принимает подключения: включите «Отладку по USB» в разделе «Для разработчиков» и перезагрузите ТВ.</span>";
  if (/timeout|timed out|i\/o/i.test(err)) return "<br><span class='small'>Проверьте IP-адрес и что компьютер и телевизор в одной сети.</span>";
  if (/не подтвердил/.test(err)) return "<br><span class='small'>Нажмите «Подключиться» ещё раз и подтвердите запрос на телевизоре.</span>";
  return "";
}

function renderRecent(list) {
  const box = $("recentBox");
  box.classList.toggle("hidden", !list.length);
  setHtml(box, list.length ? "Недавние: " + list.map((t) => `<button type="button" data-a="${esc(t.address)}" title="${esc(t.address)}">${esc(t.name || t.address)}</button>`).join("") : "");
}
$("recentBox").onclick = (e) => { if (e.target.dataset && e.target.dataset.a) connect(e.target.dataset.a); };

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
    const found = r.found || [], pairing = r.pairing || [];
    if (!found.length && !pairing.length) {
      box.innerHTML = '<div class="status">Телевизор не найден. Проверьте, что на нём включена «Отладка по USB» или «Отладка по Wi-Fi». Если на компьютере включён VPN или прокси — выключите его на время: он может перехватывать подключения к домашней сети. Можно ввести IP вручную (на ТВ: Настройки → Сеть → ваша сеть).</div>';
    } else {
      box.innerHTML = found.map((f) => `<div class="scanitem"><div><b>${esc(f.address.replace(/:5555$/, ""))}</b> <span class="muted small">${esc(f.name || "устройство с открытым ADB")}</span></div><button class="primary" data-a="${esc(f.address)}">Подключиться</button></div>`).join("")
        + pairing.map((f) => `<div class="scanitem"><div><b>${esc(f.address)}</b> <span class="muted small">телевизор показывает код сопряжения</span></div><button class="primary" data-p="${esc(f.address)}">Ввести код</button></div>`).join("");
      box.querySelectorAll("button").forEach((x) => (x.onclick = () => (x.dataset.p ? pairDialog(x.dataset.p) : connect(x.dataset.a))));
    }
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = "Найти в сети";
};

// ---------------- Сопряжение по коду (Отладка по Wi-Fi) ----------------
async function pairDialog(prefill) {
  const keep = pairDialog.keep || {};
  const body = `<ol class="steps">
      <li>На телевизоре: <b>Настройки → Для разработчиков → Отладка по Wi-Fi</b> — включите её.</li>
      <li>Откройте пункт <b>«Подключить устройство с помощью кода сопряжения»</b>. На экране появятся код из шести цифр, IP-адрес и порт.</li>
      <li>Не закрывая это окно на телевизоре, введите данные ниже.</li>
    </ol>
    <label class="field">IP-адрес и порт сопряжения<input id="pAddr" placeholder="192.168.1.50:37123" autocomplete="off" spellcheck="false" value="${esc(prefill || keep.addr || "")}"></label>
    <label class="field">Код сопряжения<input id="pCode" inputmode="numeric" maxlength="7" placeholder="123456" autocomplete="off" value="${esc(keep.code || "")}"></label>
    <label class="field">Порт подключения <span class="muted small">необязательно: если программа не найдёт его сама, возьмите на предыдущем экране «Отладка по Wi-Fi», строка «IP-адрес и порт»</span><input id="pPort" inputmode="numeric" maxlength="5" placeholder="найдём автоматически" autocomplete="off" value="${esc(keep.port || "")}"></label>`;
  const vals = () => ({ addr: $("pAddr").value.trim(), code: $("pCode").value.replace(/\D/g, ""), port: $("pPort").value.trim() });
  const ok = await modal({
    title: "Сопряжение по коду", body, ok: "Выполнить сопряжение", okClass: "primary",
    onOpen: () => {
      const check = () => { const v = vals(); $("mOk").disabled = !(/^.+:\d{1,5}$/.test(v.addr) && v.code.length === 6 && /^\d{0,5}$/.test(v.port)); };
      ["pAddr", "pCode", "pPort"].forEach((id) => ($(id).oninput = check));
      check();
      (prefill || keep.addr ? $("pCode") : $("pAddr")).focus();
    },
  });
  const v = vals();
  pairDialog.keep = ok ? v : {};
  if (!ok) return;
  toast("Выполняю сопряжение…");
  try {
    const r = await api("/api/pair", { address: v.addr, code: v.code, connectPort: v.port });
    pairDialog.keep = {};
    if (r.needPort) return portDialog(v.addr.replace(/:\d+$/, ""));
    toast("Сопряжение выполнено, подключаюсь…");
    pollState();
  } catch (e) {
    toast(e.message, true);
    if (e.status !== 409) pairDialog();
  }
}

async function portDialog(host) {
  const ok = await modal({
    title: "Сопряжение выполнено ✓",
    body: `<p>Телевизор запомнил этот компьютер. Осталось подключиться: закройте на телевизоре окно с кодом и посмотрите на экран <b>«Отладка по Wi-Fi»</b>, строка <b>«IP-адрес и порт»</b>.</p>
      <label class="field">Порт подключения для ${esc(host)}<input id="cPort" inputmode="numeric" maxlength="5" placeholder="например 41235" autocomplete="off"></label>`,
    ok: "Подключиться", okClass: "primary",
    onOpen: () => { const c = () => ($("mOk").disabled = !/^\d{2,5}$/.test($("cPort").value.trim())); $("cPort").oninput = c; c(); $("cPort").focus(); },
  });
  if (ok) connect(host + ":" + $("cPort").value.trim());
}
$("btnPair").onclick = () => pairDialog();

// ---------------- Демо-режим ----------------
$("btnDemo").onclick = async () => {
  try { await api("/api/demo", {}); pollState(); } catch (e) { toast(e.message, true); }
};

async function disconnect() {
  try { await api("/api/disconnect", {}); } catch (_) {}
  pollState();
}
$("btnDemoExit").onclick = disconnect;
$("btnDisconnect").onclick = disconnect;

function onConnected() {
  const d = S.device || {};
  const chip = $("devchip");
  chip.textContent = `${d.manufacturer || ""} ${d.model || ""} · Android ${d.android || "?"}${d.wireless ? " · отладка по Wi-Fi" : ""}`.trim();
  chip.classList.remove("hidden");
  chip.classList.toggle("demo", S.demo);
  $("demoBanner").classList.toggle("hidden", !S.demo);
  $("btnDisconnect").classList.remove("hidden");
  $("connectView").classList.add("hidden");
  $("mainView").classList.remove("hidden");
  if (d.isTv === false) toast("Похоже, это не телевизор, а телефон или планшет. Будьте внимательны.", true);
  chartData.length = 0;
  S.selected.clear(); S.selectedRem.clear(); S.packages = []; S.launchers = null;
  $("sessionCard").classList.add("hidden");
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

$("btnQuit").onclick = async () => {
  try { await api("/api/quit", {}); } catch (_) {}
  clearInterval(S.stateTimer); clearInterval(S.statsTimer);
  document.body.innerHTML = '<main class="view"><div class="hero"><h1>Программа закрыта</h1><p class="muted">Можно закрыть эту вкладку.</p></div></main>';
};

// ---------------- Обновления ----------------
function renderUpdate(s) {
  $("verInfo").textContent = "Unbrake TV " + (s.version || "");
  if (document.activeElement !== $("updChk")) $("updChk").checked = !!s.updateCheck;
  const u = s.update || {}, b = $("updateBanner");
  const show = u.newer && !S.updateDismissed;
  b.classList.toggle("hidden", !show);
  S.updateNotes = u.notes || "";
  if (show) setHtml(b, `<span>Вышла новая версия <b>Unbrake TV ${esc(u.latest)}</b> — у вас ${esc(s.version)}.</span>
    <a href="${esc(u.url)}" target="_blank" rel="noopener noreferrer">Скачать</a>
    ${u.notes ? '<button class="linkbtn" data-act="notes">Что нового</button>' : ""}<button class="x" data-act="close" title="Скрыть">×</button>`);
}
$("updateBanner").onclick = (e) => {
  const act = e.target.dataset && e.target.dataset.act;
  if (act === "close") { S.updateDismissed = true; $("updateBanner").classList.add("hidden"); }
  if (act === "notes") modal({ title: "Что нового", body: `<div class="notes">${esc(S.updateNotes)}</div>`, ok: "Закрыть", okClass: "primary", info: true });
};
$("updChk").onchange = async (e) => {
  try { await api("/api/settings", { updateCheck: e.target.checked }); toast(e.target.checked ? "Программа будет проверять обновления при запуске" : "Автоматическая проверка обновлений выключена"); }
  catch (err) { toast(err.message, true); }
};
$("btnCheckUpd").onclick = async () => {
  const b = $("btnCheckUpd");
  b.disabled = true;
  try {
    const u = await api("/api/update", {});
    S.updateDismissed = false;
    if (u.error) toast("Не удалось проверить обновления: " + u.error, true);
    else if (u.newer) toast("Доступна новая версия " + u.latest);
    else toast(`У вас актуальная версия (${S.version}).`);
    pollState();
  } catch (e) { toast(e.message, true); }
  b.disabled = false;
};

// ---------------- Вкладки ----------------
document.querySelectorAll(".tabs button").forEach((b) => (b.onclick = () => switchTab(b.dataset.tab)));
function switchTab(t) {
  S.tab = t;
  document.querySelectorAll(".tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === t));
  document.querySelectorAll(".tab").forEach((x) => x.classList.toggle("hidden", x.id !== "tab-" + t));
  if (t === "history") loadHistory();
  if (t === "install") loadLaunchers();
  if (t === "overview") requestAnimationFrame(drawChart);
  renderActionbar();
}

// ---------------- Нагрузка ----------------
const chartData = [];
async function loadStats() {
  if (S.status !== "connected" || document.hidden || S.statsBusy) return;
  S.statsBusy = true;
  let s;
  try { s = await api("/api/stats"); }
  catch (e) { if (e.status === 409) pollState(); return; }
  finally { S.statsBusy = false; }
  if (S.status !== "connected") return;
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

  setHtml($("topBody"), (s.top || []).map((p) => {
    const known = findPkg(p.package);
    const lv = lvl((known || p).level);
    const name = (known || {}).name || p.name || p.package;
    const id = known ? known.id : p.package;
    const canStop = lv !== "critical" && !S.blocked.includes(id);
    return `<tr><td><div class="appname">${esc(name)}</div><div class="pkg">${esc(p.package)}</div></td>
      <td><span class="badge ${lv}">${LEVEL[lv].t}</span></td>
      <td class="num">${p.cpu.toFixed(1)}%</td><td class="num">${fmtKB(p.memKb)}</td>
      <td class="num">${canStop ? `<button class="stopbtn" data-stop="${esc(id)}" title="Остановить приложение. Оно запустится снова, когда вы его откроете.">Остановить</button>` : ""}</td></tr>`;
  }).join("") || '<tr><td colspan="5" class="muted">Нет данных о процессах</td></tr>');

  renderSession(s);
  renderAdvice(s, memUsed, stor);
  if (S.tab === "overview") drawChart();
}

$("topBody").onclick = async (e) => {
  const id = e.target.dataset && e.target.dataset.stop;
  if (!id) return;
  e.target.disabled = true;
  try { await api("/api/app", { action: "stop", package: id }); toast("Приложение остановлено"); $("topBody")._html = ""; loadStats(); }
  catch (err) { toast(err.message, true); }
};

// What the session has achieved so far, compared with the moment of connecting.
function renderSession(s) {
  const el = $("sessionCard");
  const ram = s.memAvailKb - (s.baseMemAvailKb || s.memAvailKb);
  const disk = (s.baseStorUsedKb || s.storUsedKb) - s.storUsedKb;
  const parts = [];
  if (ram >= 20480) parts.push(`свободной оперативной памяти стало больше на <b>${fmtKB(ram)}</b>`);
  if (disk >= 20480) parts.push(`в памяти устройства освободилось <b>${fmtKB(disk)}</b>`);
  if (!s.changed || !parts.length) { el.classList.add("hidden"); return; }
  el.classList.remove("hidden");
  setHtml(el, `<h2>Результат этого сеанса</h2><div>С момента подключения ${parts.join(", ")}.</div>`);
}

// Process names like "com.google.android.gms.persistent" belong to the longest matching package.
function findPkg(proc) {
  let best = null;
  for (const p of S.packages) {
    if ((proc === p.id || proc.startsWith(p.id + ".")) && (!best || p.id.length > best.id.length)) best = p;
  }
  return best;
}

function setMetric(k, v, text, warn, bad) {
  $("m" + k).textContent = text;
  const b = $("b" + k);
  b.style.width = Math.max(0, Math.min(100, v)) + "%";
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
  if (rec.length && !recRunning.length) tips.push(`Найдено <b>${rec.length}</b> ${plural(rec.length, "приложение, которое", "приложения, которые", "приложений, которые")} безопасно удалить.`);
  const el = $("advice");
  if (!tips.length) { el.classList.add("hidden"); return; }
  el.classList.remove("hidden");
  setHtml(el, `<h2>Что можно улучшить</h2><ul>${tips.map((t) => "<li>" + t + "</li>").join("")}</ul>
    ${rec.length ? '<button class="primary" data-act="rec">Показать, что удалить</button>' : ""}`);
}
$("advice").onclick = (e) => { if (e.target.dataset && e.target.dataset.act === "rec") { setFilter("recommended"); switchTab("apps"); } };

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
      const y = pad.t + (h - pad.t - pad.b) * (1 - Math.max(0, Math.min(100, p[key])) / 100);
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
    S.packages.forEach((p) => (p.level = lvl(p.level)));
    [...S.selected].forEach((id) => { const p = S.packages.find((x) => x.id === id); if (!p || p.state !== "installed") S.selected.delete(id); });
    [...S.selectedRem].forEach((id) => { const p = S.packages.find((x) => x.id === id); if (!p || p.state === "installed") S.selectedRem.delete(id); });
    renderApps(); renderRemoved();
  } catch (e) { if (S.status === "connected") toast("Не удалось получить список приложений: " + e.message, true); }
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

// ---------------- Модальные окна ----------------
let modalClose = null;
function modal({ title, body, ok, okClass, onOpen, info }) {
  if (modalClose) modalClose(false);
  return new Promise((resolve) => {
    $("mTitle").textContent = title;
    $("mBody").innerHTML = body;
    $("mOk").textContent = ok || "OK";
    $("mOk").className = okClass || "danger";
    $("mOk").disabled = false;
    $("mCancel").classList.toggle("hidden", !!info);
    $("modal").classList.remove("hidden");
    const close = (v) => {
      modalClose = null;
      $("modal").classList.add("hidden");
      $("mOk").onclick = $("mCancel").onclick = null;
      resolve(v);
    };
    modalClose = close;
    $("mCancel").onclick = () => close(false);
    $("mOk").onclick = () => {
      const m = document.querySelector('input[name="method"]:checked');
      S.lastMethod = m ? m.value : "uninstall";
      close(true);
    };
    onOpen && onOpen();
  });
}
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && modalClose) modalClose(false); });

function groupHtml(level, items, note) {
  if (!items.length) return "";
  return `<div class="group ${level}"><span class="badge ${level}">${LEVEL[level].t}</span> ${note}
    <ul>${items.map((p) => `<li><b>${esc(p.name)}</b> <span class="pkg">${esc(p.id)}</span>${level === "critical" || level === "caution" ? `<div class="reason">${esc(p.reason)}</div>` : ""}</li>`).join("")}</ul></div>`;
}

// ---------------- Подтверждение (защита от дурака) ----------------
async function confirmRemove() {
  const pk = [...S.selected].map((id) => S.packages.find((p) => p.id === id)).filter(Boolean);
  if (!pk.length) return;
  const by = (l) => pk.filter((p) => p.level === l);
  const crit = by("critical"), warn = [...by("caution"), ...by("unknown")];
  const user = pk.filter((p) => !p.system);
  const launchers = S.packages.filter((p) => p.isLauncher && p.state === "installed");
  const noLauncher = launchers.length > 0 && launchers.every((p) => S.selected.has(p.id));
  const needPhrase = crit.length > 0 || noLauncher;

  let body = `<p>Будет удалено приложений: <b>${pk.length}</b>. Системные приложения удаляются только для пользователя — их можно вернуть на вкладке «Удалённые».</p>`;
  if (noLauncher) body += `<div class="group critical"><b>⚠ Вы удаляете все рабочие столы.</b> После нажатия «Домой» будет пустой экран. Сначала установите другой рабочий стол на вкладке «Установка».</div>`;
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
  if (!ok) return;
  const method = S.lastMethod || "uninstall";
  const done = await runChange("/api/remove", { packages: pk.map((p) => p.id), accepted: true, confirm: needPhrase ? S.phrase : "", method }, "Удаление");
  if (done) { S.selected.clear(); renderApps(); }
}

async function doRestore() {
  const ids = [...S.selectedRem];
  if (!ids.length) return;
  if (await runChange("/api/restore", { packages: ids }, "Восстановление")) { S.selectedRem.clear(); renderRemoved(); }
}

async function runChange(path, body, title) {
  $("btnAction").disabled = true;
  $("selInfo").innerHTML = '<span class="spin"></span>Выполняю…';
  let done = false;
  try {
    const r = await api(path, body);
    const res = r.results || [];
    const okN = res.filter((x) => x.ok).length;
    const html = `<p>Успешно: <b>${okN}</b> из ${res.length}</p>` + res.map((x) => {
      const p = S.packages.find((q) => q.id === x.id) || { name: x.id };
      const how = x.method !== "disable" ? "" : body.method === "disable" ? " (отключено)" : " (отключено — удалить не разрешено, но работать оно не будет)";
      return `<div class="res"><span class="${x.ok ? "ok" : "bad"}">${x.ok ? "✓" : "✗"}</span><div><b>${esc(p.name)}</b>${how}<div class="pkg">${esc(x.id)}${x.ok ? "" : " — " + esc(x.output)}</div></div></div>`;
    }).join("") + (okN && path.includes("remove") ? '<p class="small muted">Перезагрузите телевизор, чтобы освободить память.</p>' : "");
    done = true;
    await modal({ title: title + " завершено", body: html, ok: "Готово", okClass: "primary", info: true });
  } catch (e) {
    toast(e.message, true);
  }
  $("btnAction").disabled = false;
  await loadPackages();
  return done;
}

// ---------------- Журнал ----------------
const ACTIONS = { remove: "Удаление", restore: "Восстановление", tweak: "Улучшение", install: "Установка" };
async function loadHistory() {
  try {
    const r = await api("/api/history");
    const h = r.history || [];
    $("historyList").innerHTML = h.length ? h.map((e) => {
      const res = e.results || [];
      const ok = res.filter((x) => x.ok).length;
      const line = (x) => x.method === "tweak" ? x.output : x.method === "install" ? x.id + (x.ok ? "" : " — " + x.output) : x.id + (x.method === "disable" ? " (отключено)" : "");
      return `<details class="card hist"><summary><b>${ACTIONS[e.action] || esc(e.action)}</b> · ${esc(e.time)} · ${esc(e.device)} · успешно ${ok} из ${res.length}</summary>
        ${res.map((x) => `<div class="res"><span class="${x.ok ? "ok" : "bad"}">${x.ok ? "✓" : "✗"}</span><span class="pkg">${esc(line(x))}</span></div>`).join("")}</details>`;
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
  reboot: { title: "Перезагрузить телевизор?", body: "<p>Телевизор перезагрузится. Программа подождёт и подключится к нему снова сама — обычно это занимает около минуты.</p>", ok: "Перезагрузить" },
  adb_off: { title: "Выключить отладку?", body: "<p>Телевизор перестанет принимать подключения по сети, и программа отключится. Чтобы подключиться снова, включите отладку в разделе «Для разработчиков».</p>", ok: "Выключить" },
};
document.querySelectorAll("[data-tweak]").forEach((b) => (b.onclick = async () => {
  const a = b.dataset.tweak, c = TWEAK_CONFIRM[a];
  if (c && !(await modal({ ...c, okClass: "primary" }))) return;
  const old = b.textContent;
  b.disabled = true; b.innerHTML = '<span class="spin"></span>Подождите…';
  try {
    const r = await api("/api/tweak", { action: a });
    toast(r.message);
    if (c) await pollState();
    else if (a === "trim_caches") loadPackages();
    else loadStats();
  } catch (e) { toast(e.message, true); }
  b.disabled = false; b.textContent = old;
}));

// ---------------- Профили ----------------
$("btnExport").onclick = () => {
  const removed = S.packages.filter((p) => p.state !== "installed").map((p) => p.id).sort();
  if (!removed.length) return toast("Пока нечего сохранять — на телевизоре ничего не удалено", true);
  const d = S.device || {};
  const profile = { app: "Unbrake TV", format: 1, created: new Date().toISOString(), device: { manufacturer: d.manufacturer, model: d.model, android: d.android }, packages: removed };
  download(`unbrake-${fileTag()}.json`, JSON.stringify(profile, null, 2));
  toast(`Профиль сохранён: ${apps(removed.length)}`);
};
$("btnImport").onclick = () => $("importFile").click();
$("importFile").onchange = async (e) => {
  const f = e.target.files[0];
  e.target.value = "";
  if (!f) return;
  let prof;
  try { prof = JSON.parse(await f.text()); } catch (_) { return toast("Это не файл профиля Unbrake TV", true); }
  const ids = prof && Array.isArray(prof.packages) ? prof.packages.filter((x) => typeof x === "string") : [];
  if (!ids.length) return toast("В профиле нет приложений", true);
  const n = markPackages(ids);
  toast(n ? `Отмечено ${n} из ${ids.length}. Проверьте список и нажмите «Удалить выбранные».` : "Все приложения из профиля уже удалены или отсутствуют на этом ТВ");
};

// Ticks installed, removable packages and shows them. Returns how many were ticked.
function markPackages(ids) {
  let n = 0;
  ids.forEach((id) => {
    const p = S.packages.find((x) => x.id === id && x.state === "installed");
    if (p && !S.blocked.includes(id)) { S.selected.add(id); n++; }
  });
  if (n) { S.search = ""; $("search").value = ""; S.onlyRunning = false; $("onlyRunning").checked = false; setFilter("all"); }
  return n;
}

// A full list of the TV's packages: attach it to a bug report to extend the knowledge base.
$("btnReport").onclick = () => {
  if (!S.packages.length) return toast("Список приложений ещё не загружен", true);
  const d = S.device || {};
  const report = {
    app: "Unbrake TV", version: S.version, kind: "tv-report", created: new Date().toISOString(),
    device: { manufacturer: d.manufacturer, model: d.model, android: d.android, sdk: d.sdk, abi: d.abi },
    packages: S.packages.map((p) => ({ id: p.id, level: p.level, system: p.system, state: p.state })),
  };
  download(`unbrake-report-${fileTag()}.json`, JSON.stringify(report, null, 2));
  const unk = S.packages.filter((p) => p.level === "unknown").length;
  toast(`Отчёт сохранён: ${apps(S.packages.length)}, из них неизвестных — ${unk}. В файле нет личных данных.`);
};

// ---------------- Готовые профили ----------------
function presetMatches(pr) {
  const pats = pr.packages || [];
  const hit = (id) => pats.some((x) => (x.endsWith("*") ? id.startsWith(x.slice(0, -1)) : x === id));
  return S.packages.filter((p) => p.state === "installed" && !S.blocked.includes(p.id) && p.level !== "critical"
    && ((pr.allRecommended && p.level === "recommended") || hit(p.id)));
}

$("btnPresets").onclick = async () => {
  let r;
  try { r = await api("/api/presets"); } catch (e) { return toast(e.message, true); }
  const list = (r.presets || []).map((pr) => ({ pr, found: presetMatches(pr) }));
  const def = (list.find((x) => x.pr.id === r.suggested && x.found.length) || list.find((x) => x.found.length) || {}).pr;
  const body = `<p>Профиль только отмечает приложения в списке. Вы увидите, что именно выбрано, и подтвердите удаление — все предупреждения и защита работают как обычно.</p>` +
    list.map(({ pr, found }) => `<label class="preset ${found.length ? "" : "off"}">
      <input type="radio" name="preset" value="${esc(pr.id)}" ${found.length ? "" : "disabled"} ${def && def.id === pr.id ? "checked" : ""}>
      <span><b>${esc(pr.title)}</b>${pr.id === r.suggested && pr.vendors && pr.vendors.length ? ' <span class="badge recommended">для вашего ТВ</span>' : ""}<br><span class="small muted">${esc(pr.description)}</span></span>
      <span class="cnt">${found.length ? "на этом ТВ: " + found.length : "нечего удалять"}</span></label>`).join("");
  const ok = await modal({
    title: "Готовые профили", body, ok: "Отметить приложения", okClass: "primary",
    onOpen: () => { $("mOk").disabled = !def; },
  });
  if (!ok) return;
  const sel = document.querySelector('input[name="preset"]:checked');
  const item = sel && list.find((x) => x.pr.id === sel.value);
  if (!item) return;
  S.selected.clear();
  const n = markPackages(item.found.map((p) => p.id));
  toast(`Профиль «${item.pr.title}»: отмечено ${apps(n)}. Проверьте список и нажмите «Удалить выбранные».`);
};

// ---------------- Установка: рабочий стол и APK ----------------
async function loadLaunchers() {
  try { S.launchers = await api("/api/launchers"); renderLaunchers(); }
  catch (e) { if (S.status === "connected") toast(e.message, true); }
}

function renderLaunchers() {
  const L = S.launchers;
  if (!L) return;
  const installed = L.installed || [], names = L.names || {};
  const title = (id) => (S.packages.find((p) => p.id === id) || {}).name || names[id] || id;
  $("curLauncher").textContent = L.current ? "сейчас: " + title(L.current) : "";
  $("launcherList").innerHTML = (L.catalog || []).map((l) => {
    const has = installed.includes(l.package), cur = L.current === l.package;
    return `<div class="launcher"><h3>${esc(l.name)} ${cur ? '<span class="badge recommended">основной</span>' : has ? '<span class="badge optional">установлен</span>' : ""}</h3>
      <p>${esc(l.description)}</p>
      <div class="btns">
        <button class="primary small-btn" data-install="${esc(l.id)}">${has ? "Обновить" : "Установить"}</button>
        ${has && !cur ? `<button class="ghost small-btn" data-home="${esc(l.package)}">Сделать основным</button>` : ""}
        ${l.store && !has ? `<button class="ghost small-btn" data-store="${esc(l.id)}" title="Откроет страницу приложения в Play Маркете на экране телевизора — установку подтвердите пультом">Открыть в Play Маркете на ТВ</button>` : ""}
        ${l.site ? `<a href="${esc(l.site)}" target="_blank" rel="noopener noreferrer">страница разработчика</a>` : ""}
      </div></div>`;
  }).join("") || '<div class="muted">Список приложений будет расширяться. Если вы хотите добавить своё приложение в магазин — свяжитесь с разработчиком.</div>';
  $("installedLaunchers").innerHTML = installed.length ? `<div class="muted small" style="margin-bottom:4px">Рабочие столы на телевизоре</div>` + installed.map((id) => `<div class="lrow">
      <div><b>${esc(title(id))}</b> <span class="pkg">${esc(id)}</span></div>
      ${id === L.current ? '<span class="badge recommended">основной</span>' : `<button class="ghost small-btn" data-home="${esc(id)}">Сделать основным</button>`}</div>`).join("") : "";
}

async function setHome(pkg) {
  try { await api("/api/launcher/home", { package: pkg }); toast("Готово: рабочий стол изменён. Нажмите «Домой» на пульте, чтобы проверить."); }
  catch (e) { toast(e.message, true); }
  await loadPackages();
  loadLaunchers();
}

document.getElementById("tab-install").addEventListener("click", async (e) => {
  const d = e.target.dataset || {};
  if (d.home) return setHome(d.home);
  if (d.store) {
    try { await api("/api/launcher/store", { id: d.store }); toast("На экране телевизора открыт Play Маркет — нажмите «Установить» пультом."); }
    catch (err) { toast(err.message, true); }
    return;
  }
  if (d.install) installLauncher(d.install);
});

async function withProgress(fn) {
  const box = $("installProgress");
  box.className = "status";
  box.innerHTML = '<span class="spin"></span><span id="progText">Подождите…</span>';
  document.querySelectorAll("#tab-install button").forEach((b) => (b.disabled = true));
  const t = setInterval(pollState, 400);
  try { return await fn(); }
  finally {
    clearInterval(t);
    box.className = "status hidden";
    document.querySelectorAll("#tab-install button").forEach((b) => (b.disabled = false));
  }
}
function renderProgress(s) { const p = $("progText"); if (p && s.progress) p.textContent = s.progress; }

async function installLauncher(id) {
  const l = ((S.launchers || {}).catalog || []).find((x) => x.id === id);
  if (!l) return;
  const src = S.demo ? "В демо-режиме ничего не скачивается: эмулятору передаётся файл-заглушка." : `Программа скачает последнюю версию со страницы разработчика на GitHub (${esc(l.repo)}) и установит её на телевизор. Нужен интернет на компьютере.`;
  if (!(await modal({ title: `Установить ${l.name}?`, body: `<p>${src}</p><p class="small muted">Заводской рабочий стол останется на месте — вы сможете вернуться к нему в любой момент.</p>`, ok: "Установить", okClass: "primary" }))) return;
  let r;
  try { r = await withProgress(() => api("/api/launcher/install", { id })); }
  catch (e) { return toast(e.message, true); }
  await loadPackages();
  await loadLaunchers();
  if (!r.package) return toast(`${l.name} установлен.`);
  if ((S.launchers || {}).current === r.package) return toast(`${l.name} обновлён.`);
  const ok = await modal({
    title: `${l.name} установлен ✓`,
    body: "<p>Сделать его основным рабочим столом? Он будет открываться по кнопке «Домой».</p><p class='small muted'>Вернуть прежний можно на этой же вкладке.</p>",
    ok: "Сделать основным", okClass: "primary",
  });
  if (ok) setHome(r.package);
}

$("btnApk").onclick = () => $("apkFile").click();
$("apkFile").onchange = async (e) => {
  const f = e.target.files[0];
  e.target.value = "";
  if (!f) return;
  if (!/\.apk$/i.test(f.name)) return toast("Выберите файл с расширением .apk", true);
  if (!(await modal({
    title: "Установить приложение?",
    body: `<p>Файл <b>${esc(f.name)}</b> (${fmtB(f.size)}) будет передан на телевизор и установлен.</p><p class="small muted">Устанавливайте только те приложения, источнику которых доверяете: программа не проверяет содержимое файла.</p>`,
    ok: "Установить", okClass: "primary",
  }))) return;
  try {
    await withProgress(() => api("/api/install", f, { "Content-Type": "application/vnd.android.package-archive", "X-Filename": encodeURIComponent(f.name) }));
    toast(`Приложение из файла ${f.name} установлено.`);
  } catch (err) { toast(err.message, true); }
  await loadPackages();
  loadLaunchers();
};

// ---------------- Старт ----------------
if (!TOKEN) document.body.innerHTML = '<main class="view"><h1>Откройте программу Unbrake TV заново</h1><p class="muted">Эта страница открыта без ключа доступа.</p></main>';
else { pollState(); S.stateTimer = setInterval(pollState, 1500); }
