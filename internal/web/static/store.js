"use strict";
// The app catalog: what can be installed on the TV and where the list comes from.

Object.assign(S, { trials: [], backups: {}, snapshots: [], range: 60, recent: [], promptedTrials: false });

const resHtml = (res, note) => `<p>Успешно: <b>${res.filter((x) => x.ok).length}</b> из ${res.length}</p>` + res.map((x) => {
  const p = S.packages.find((q) => q.id === x.id) || { name: x.id };
  const how = x.method === "backup" ? " (установлено из сохранённой копии)" : x.method === "tweak" ? "" : "";
  return `<div class="res"><span class="${x.ok ? "ok" : "bad"}">${x.ok ? "✓" : "✗"}</span><div><b>${esc(x.method === "tweak" ? x.output : p.name)}</b>${how}<div class="pkg">${esc(x.id)}${x.ok ? "" : " — " + esc(x.output)}</div></div></div>`;
}).join("") + (note || "");

const pkgName = (id) => (S.packages.find((p) => p.id === id) || {}).name || id;
const fmtWhen = (unix) => new Date(unix * 1000).toLocaleString("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" });

// ---------------- Установка: каталог из репозитория ----------------
function renderLaunchers() {
  const L = S.launchers;
  if (!L) return;
  const installed = L.installed || [], names = L.names || {}, have = L.have || {};
  const title = (id) => (S.packages.find((p) => p.id === id) || {}).name || names[id] || id;
  $("curLauncher").textContent = L.current ? "сейчас: " + title(L.current) : "";
  const card = (l) => {
    const has = have[l.id], cur = l.package && L.current === l.package;
    return `<div class="launcher"><h3>${esc(l.name)} ${cur ? '<span class="badge recommended">основной</span>' : has ? '<span class="badge optional">установлен</span>' : ""}${l.source === "custom" ? ' <span class="badge unknown">ваш</span>' : ""}</h3>
      <p>${esc(l.description)}</p>
      <div class="btns">
        <button class="primary small-btn" data-install="${esc(l.id)}">${has ? "Обновить" : "Установить"}</button>
        ${l.kind === "launcher" && has && !cur ? `<button class="ghost small-btn" data-home="${esc(l.package)}">Сделать основным</button>` : ""}
        ${l.store && l.package && !has ? `<button class="ghost small-btn" data-store="${esc(l.id)}" title="Откроет страницу приложения в Play Маркете на экране телевизора — установку подтвердите пультом">Открыть в Play Маркете на ТВ</button>` : ""}
        ${l.site ? `<a href="${esc(l.site)}" target="_blank" rel="noopener noreferrer">страница разработчика</a>` : ""}
        <button class="linkbtn small muted" data-remove="${esc(l.id)}" data-name="${esc(l.name)}" data-custom="${l.source === "custom" ? 1 : ""}">убрать из списка</button>
      </div></div>`;
  };
  const cat = L.catalog || [];
  const ls = cat.filter((l) => l.kind === "launcher"), as = cat.filter((l) => l.kind !== "launcher");
  $("launcherList").innerHTML = ls.map(card).join("") || '<div class="muted">Список приложений будет расширяться. Если вы хотите добавить своё приложение в магазин — свяжитесь с разработчиком.</div>';
  $("appList").innerHTML = as.map(card).join("") || '<div class="muted">Список приложений будет расширяться. Если вы хотите добавить своё приложение в магазин — свяжитесь с разработчиком.</div>';
  $("installedLaunchers").innerHTML = installed.length ? `<div class="muted small" style="margin-bottom:4px">Рабочие столы на телевизоре</div>` + installed.map((id) => `<div class="lrow">
      <div><b>${esc(title(id))}</b> <span class="pkg">${esc(id)}</span></div>
      ${id === L.current ? '<span class="badge recommended">основной</span>' : `<button class="ghost small-btn" data-home="${esc(id)}">Сделать основным</button>`}</div>`).join("") : "";
  const st = L.store || {};
  const link = `<a href="https://github.com/${esc(st.repo)}" target="_blank" rel="noopener noreferrer">${esc(st.repo)}</a>`;
  const when = st.updated ? new Date(st.updated * 1000).toLocaleString("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" }) : "";
  const note = st.stale && when ? ` <span class="warn">Не удалось обновить список: ${esc(st.error)} Показан список от ${esc(when)}.</span>`
    : st.error ? ` <span class="warn">${esc(st.error)}</span>` : "";
  $("storeInfo").innerHTML = S.demo ? "В демо-режиме показаны примеры, из интернета ничего не скачивается."
    : st.active ? `Список приложений берётся из репозитория ${link} на GitHub.${note}`
      : `Репозиторий приложений ${link} пока недоступен.${note}`;
  const rb = $("btnResetCat");
  rb.classList.toggle("hidden", !L.hidden);
  rb.textContent = `Вернуть скрытые (${L.hidden})`;
}

document.getElementById("tab-install").addEventListener("click", async (e) => {
  const d = e.target.dataset || {};
  if (!d.remove) return;
  if (!(await modal({ title: `Убрать «${d.name}» из списка?`, body: d.custom ? "<p>Этот источник добавили вы — он будет удалён из списка. С телевизора ничего не удаляется.</p>" : "<p>Запись будет скрыта на этом компьютере. Вернуть её можно кнопкой «Вернуть скрытые». С телевизора ничего не удаляется.</p>", ok: "Убрать", okClass: "primary" }))) return;
  try { await api("/api/catalog", { action: "remove", id: d.remove }); } catch (err) { toast(err.message, true); }
  loadLaunchers();
});
$("btnResetCat").onclick = async () => { try { await api("/api/catalog", { action: "reset" }); } catch (e) { toast(e.message, true); } loadLaunchers(); };
$("btnRefreshCat").onclick = async () => {
  const b = $("btnRefreshCat");
  b.disabled = true;
  try { S.launchers = await api("/api/launchers?refresh=1"); renderLaunchers(); toast("Список обновлён"); } catch (e) { toast(e.message, true); }
  b.disabled = false;
};

$("btnAddApp").onclick = async () => {
  const keep = $("btnAddApp")._keep || {};
  const ok = await modal({
    title: "Добавить свой источник", ok: "Добавить", okClass: "primary",
    body: `<p>Подойдёт любой проект на GitHub, который выкладывает APK-файлы в разделе «Releases». Программа будет ставить последнюю версию и сама выберет сборку под процессор телевизора.</p>
      <label class="field">Репозиторий или ссылка на него<input id="aRepo" placeholder="https://github.com/владелец/название" autocomplete="off" spellcheck="false" value="${esc(keep.repo || "")}"></label>
      <label class="field">Название <span class="muted small">необязательно</span><input id="aName" maxlength="100" autocomplete="off" value="${esc(keep.name || "")}"></label>
      <label class="field">Описание <span class="muted small">необязательно</span><input id="aDesc" maxlength="300" autocomplete="off" value="${esc(keep.description || "")}"></label>
      <div class="field">Что это<div class="btnrow"><label class="chk"><input type="radio" name="aKind" value="app" ${keep.kind === "launcher" ? "" : "checked"}> приложение</label><label class="chk"><input type="radio" name="aKind" value="launcher" ${keep.kind === "launcher" ? "checked" : ""}> рабочий стол</label></div></div>`,
    onOpen: () => { const c = () => ($("mOk").disabled = !$("aRepo").value.trim()); $("aRepo").oninput = c; c(); $("aRepo").focus(); },
  });
  const v = { repo: $("aRepo").value.trim(), name: $("aName").value.trim(), description: $("aDesc").value.trim(), kind: (document.querySelector('input[name="aKind"]:checked') || {}).value || "app" };
  $("btnAddApp")._keep = ok ? v : {};
  if (!ok) return;
  toast("Проверяю репозиторий…");
  try { await api("/api/catalog", { action: "add", ...v }); $("btnAddApp")._keep = {}; toast("Добавлено в список"); loadLaunchers(); }
  catch (e) { toast(e.message, true); if (e.status !== 409) $("btnAddApp").click(); }
};

$("btnStore").onclick = async () => {
  const st = (S.launchers || {}).store || {};
  const ok = await modal({
    title: "Репозиторий приложений", ok: "Сохранить", okClass: "primary",
    body: `<p>Список на этой вкладке берётся из репозитория на GitHub. Чтобы приложение появилось у всех, кто пользуется программой, создайте в репозитории папку и положите в неё APK-файл и описание — программа увидит их сама.</p>
      <ul class="steps"><li><b>Папка</b> в корне репозитория или внутри <b>apps</b> — одно приложение.</li>
      <li><b>APK-файл</b> в папке — то, что установится. Можно положить несколько сборок под разные процессоры: программа выберет нужную.</li>
      <li><b>description.txt</b> или <b>README.md</b> — описание, которое увидят в списке.</li>
      <li><b>info.json</b> (необязательно) — отметить рабочий стол, указать имя пакета или дать ссылку на чужой проект, чтобы всегда ставилась его последняя версия.</li></ul>
      <label class="field">Репозиторий<input id="sRepo" placeholder="владелец/название" autocomplete="off" spellcheck="false" value="${esc(st.repo || "")}"></label>
      <p class="small muted">Оставьте поле пустым, чтобы вернуть репозиторий по умолчанию.</p>`,
  });
  if (!ok) return;
  toast("Читаю репозиторий…");
  try {
    const r = await api("/api/catalog", { action: "store", repo: $("sRepo").value.trim() });
    toast(r.warning ? r.warning : "Репозиторий приложений изменён", !!r.warning);
  } catch (e) { toast(e.message, true); }
  loadLaunchers();
};

async function installLauncher(id) {
  const l = ((S.launchers || {}).catalog || []).find((x) => x.id === id);
  if (!l) return;
  const from = l.file || l.asset ? `из репозитория приложений (${esc(((S.launchers || {}).store || {}).repo || "")})` : `со страницы разработчика на GitHub (${esc(l.repo)})`;
  const src = S.demo ? "В демо-режиме ничего не скачивается: эмулятору передаётся файл-заглушка." : `Программа скачает установочный файл ${from} и установит его на телевизор. Нужен интернет на компьютере.`;
  const note = l.kind === "launcher" ? '<p class="small muted">Заводской рабочий стол останется на месте — вы сможете вернуться к нему в любой момент.</p>' : "";
  if (!(await modal({ title: `Установить ${l.name}?`, body: `<p>${src}</p>${note}`, ok: "Установить", okClass: "primary" }))) return;
  let r;
  try { r = await withProgress(() => api("/api/launcher/install", { id })); }
  catch (e) { return toast(e.message, true); }
  await loadPackages();
  await loadLaunchers();
  if (!r.launcher || !r.package) return toast(`${l.name} установлен.`);
  if ((S.launchers || {}).current === r.package) return toast(`${l.name} обновлён.`);
  const ok = await modal({
    title: `${l.name} установлен ✓`,
    body: "<p>Сделать его основным рабочим столом? Он будет открываться по кнопке «Домой».</p><p class='small muted'>Вернуть прежний можно на этой же вкладке.</p>",
    ok: "Сделать основным", okClass: "primary",
  });
  if (ok) setHome(r.package);
}

