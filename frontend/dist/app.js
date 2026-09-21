// Interface logic.
//
// The Go side owns the library, the artist reduction and the plan. This file
// holds only what the user has chosen — which names to merge, which tracks are
// selected, what was typed into the bulk editor — and asks Go to price those
// choices after every change, so the footer always shows what would really
// happen to the files.

const go = () => window.go.main.App;
const $ = (id) => document.getElementById(id);

const state = {
  root: "",
  onPhone: false,
  summary: null,
  tracks: [],

  // Names in the files that must NOT be folded into their artist.
  detached: new Set(),
  // Artists the user pointed at another name, keyed by the proposed name.
  renamed: new Map(),
  // Single names in the files aimed somewhere else than their own artist:
  // "Future, Juice Wrld" put under Juice WRLD while Future itself stays.
  moved: new Map(),

  albumArtistMode: "keep",
  removeCompilation: false,
  removeSort: false,
  removeLegacy: false,
  shrinkArtwork: false,
  backup: false,

  busy: false,
};

/* Helpers ------------------------------------------------------------------ */

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
const files = (n) => plural(n, "file");
const tracks = (n) => plural(n, "track");

function duration(seconds) {
  if (!seconds) return "";
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

const baseName = (path) => String(path).split(/[\\/]/).pop();

let toastTimer = null;
function toast(message, isError) {
  const node = $("toast");
  node.textContent = message;
  node.classList.toggle("error", Boolean(isError));
  node.hidden = false;

  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { node.hidden = true; }, isError ? 7000 : 3000);
}

function debounce(fn, ms) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}

/* Theme -------------------------------------------------------------------- */

// The chosen theme is remembered per machine; with nothing remembered the
// system preference decides. Storage can throw in a locked-down webview, so
// neither reading nor writing it is allowed to break the page.
const THEME_KEY = "mlo-theme";

function readTheme() {
  try {
    const saved = localStorage.getItem(THEME_KEY);
    if (saved === "light" || saved === "dark") return saved;
  } catch {}
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {}
}

applyTheme(readTheme());

$("theme").addEventListener("click", () => {
  applyTheme(document.documentElement.dataset.theme === "light" ? "dark" : "light");
});

/* Navigation --------------------------------------------------------------- */

function showView(name) {
  for (const nav of document.querySelectorAll(".nav")) {
    nav.setAttribute("aria-current", String(nav.dataset.view === name));
  }
  for (const view of document.querySelectorAll(".view")) {
    view.hidden = view.dataset.view !== name;
  }
  if (name === "tracks") renderTracks();
}

for (const nav of document.querySelectorAll(".nav")) {
  nav.addEventListener("click", () => showView(nav.dataset.view));
}

/* Source ------------------------------------------------------------------- */

const phone = { serial: "", path: "/sdcard/Music" };

$("pick").addEventListener("click", showSource);

function showSource() {
  // The source screen is the way a library is opened, so the topbar button
  // that leads here has nothing left to do while it is up — and a button that
  // reopens the screen you are already on reads as broken.
  $("pick").hidden = true;

  $("source").hidden = false;
  $("scanning").hidden = true;
  $("workspace").hidden = true;
  $("footer").hidden = true;
}

$("src-folder").addEventListener("click", async () => {
  try {
    const chosen = await go().ChooseFolder();
    if (chosen) {
      phone.serial = "";
      startScan(chosen);
    }
  } catch (err) {
    toast(String(err), true);
  }
});

$("src-phone").addEventListener("click", () => {
  $("src-phone").setAttribute("aria-pressed", "true");
  $("phone-panel").hidden = false;
  loadPhones();
});

$("phone-refresh").addEventListener("click", () => { loadPhones(); loadFolders(); });
$("phone-scan").addEventListener("click", startPhoneScan);

$("phone-path").addEventListener("change", () => {
  phone.path = $("phone-path").value.trim() || "/sdcard/Music";
  loadFolders();
});

$("phone-up").addEventListener("click", () => {
  const parts = phone.path.replace(/\/+$/, "").split("/");
  if (parts.length > 2) {
    parts.pop();
    setPhonePath(parts.join("/") || "/");
  }
});

function setPhonePath(value) {
  phone.path = value;
  $("phone-path").value = value;
  loadFolders();
}

async function loadPhones() {
  const list = $("phone-devices");
  list.replaceChildren(el("div", "row-item muted", "Looking for a phone…"));

  let status;
  try {
    status = await go().Phones();
  } catch (err) {
    list.replaceChildren();
    showPhoneHint(String(err));
    return;
  }

  showPhoneHint(status.hint);
  list.replaceChildren();

  const devices = status.devices || [];
  // With one phone plugged in there is nothing to choose between, so it is
  // selected and its folders listed straight away.
  const ready = devices.filter((d) => d.ready);
  const autoSelect = !phone.serial && ready.length === 1;
  if (autoSelect) phone.serial = ready[0].serial;

  for (const found of devices) list.append(deviceRow(found));
  $("phone-scan").disabled = !phone.serial;
  if (autoSelect) loadFolders();
}

function deviceRow(found) {
  const row = el("button", found.ready ? "device ready" : "device");
  row.disabled = !found.ready;
  row.setAttribute("aria-pressed", String(found.serial === phone.serial));

  const main = el("div", "row-main");
  main.append(
    el("div", "row-name", found.model || found.serial),
    el("div", "row-sub", found.ready ? found.serial : found.hint || found.state),
  );

  row.append(el("span", "dot"), main);
  row.addEventListener("click", () => { phone.serial = found.serial; loadPhones(); loadFolders(); });
  return row;
}

function showPhoneHint(text) {
  const hint = $("phone-hint");
  hint.hidden = !text;
  hint.textContent = text || "";
}

async function loadFolders() {
  const list = $("phone-folders");
  if (!phone.serial) {
    list.replaceChildren();
    return;
  }

  list.replaceChildren(el("div", "row-item muted", "Reading the folder…"));
  try {
    const names = await go().PhoneFolders(phone.serial, phone.path);
    list.replaceChildren();
    for (const name of names || []) {
      const row = el("div", "row-item");
      row.append(el("div", "row-main", name));
      row.addEventListener("click", () => setPhonePath(`${phone.path.replace(/\/+$/, "")}/${name}`));
      list.append(row);
    }
  } catch (err) {
    list.replaceChildren();
    showPhoneHint(String(err));
  }
  $("phone-scan").disabled = !phone.serial;
}

/* Scanning ----------------------------------------------------------------- */

$("rescan").addEventListener("click", rescan);
$("scan-cancel").addEventListener("click", () => go().Cancel());

function rescan() {
  if (!state.root) return;
  if (phone.serial) startPhoneScan();
  else startScan(state.root);
}

function beginScanScreen(root, status) {
  state.root = root;
  $("root-path").textContent = root;
  $("root-path").classList.remove("muted");
  $("rescan").hidden = false;

  $("source").hidden = true;
  $("workspace").hidden = true;
  $("footer").hidden = true;
  $("scanning").hidden = false;
  $("scan-bar").style.width = "0";
  $("scan-status").textContent = status;
  $("scan-current").textContent = "";
}

async function startScan(root) {
  beginScanScreen(root, "Looking for files…");
  try {
    applyScan(await go().Scan(root));
  } catch (err) {
    $("scanning").hidden = true;
    showSource();
    toast(String(err), true);
  }
}

async function startPhoneScan() {
  beginScanScreen(phone.path, "Preparing the phone…");
  try {
    applyScan(await go().ScanPhone(phone.serial, phone.path));
  } catch (err) {
    $("scanning").hidden = true;
    showSource();
    showPhoneHint(String(err));
    toast(String(err), true);
  }
}

window.runtime.EventsOn("scan:progress", (p) => {
  $("scan-bar").style.width = p.total ? `${(p.done / p.total) * 100}%` : "0";
  $("scan-status").textContent = `Read ${p.done} of ${files(p.total)}`;
  $("scan-current").textContent = p.current || "";
});

async function applyScan(result) {
  const summary = result.summary;
  for (const field of ["targets", "albumArtists"]) summary[field] = summary[field] || [];

  state.summary = summary;
  state.onPhone = result.source === "phone";
  state.detached.clear();
  state.renamed.clear();
  state.moved.clear();
  expanded.clear();
  selection.clear();

  try {
    state.tracks = (await go().Tracks()) || [];
  } catch {
    state.tracks = [];
  }

  $("scanning").hidden = true;
  $("pick").hidden = false;
  $("workspace").hidden = false;
  $("footer").hidden = false;
  $("revert").hidden = true;

  renderArtists();
  renderCleanup(result);
  renderTracks();
  updateCounts();
  showView("artists");
  refreshPending();

  if (result.cancelled) toast("Scan stopped — showing part of the library");
}

function updateCounts() {
  $("count-tracks").textContent = state.tracks.length || "";
  const missing = state.summary.trackCount - state.summary.withLyrics;
  $("count-lyrics").textContent = missing > 0 ? missing : "";
}

/* Rules -------------------------------------------------------------------- */

// renames turns the artist list into the map the plan needs: every name in the
// files pointed at the artist it ends up under, minus the ones detached.
function renames() {
  const out = {};
  for (const group of groups) {
    for (const source of group.sources) {
      if (source.value !== group.name) out[source.value] = group.name;
    }
  }
  return out;
}

function rules() {
  return {
    rename: renames(),
    albumArtistMode: state.albumArtistMode,
    removeCompilation: state.removeCompilation,
    removeSort: state.removeSort,
    removeLegacy: state.removeLegacy,
    shrinkArtwork: state.shrinkArtwork,
    backup: state.backup,
  };
}

/* Artists ------------------------------------------------------------------ */

$("artist-search").addEventListener("input", renderArtists);

$("artists-all").addEventListener("click", () => {
  state.detached.clear();
  renderArtists();
  refreshPending();
});

$("artists-none").addEventListener("click", () => {
  for (const target of state.summary.targets) {
    for (const source of target.sources) state.detached.add(source.value);
  }
  renderArtists();
  refreshPending();
});

// targetName is what an artist ends up called, once the renames apply.
function targetName(target) {
  return state.renamed.get(target.name) || target.name;
}

// buildGroups works out the artist list as it will be. Two artists pointed at
// the same name become one row, which is how "put Future under Juice WRLD"
// shows its result straight away instead of waiting for the next scan.
function buildGroups() {
  const nameOf = new Map(); // name in the files -> artist it ends up under
  const found = new Map();

  const ensure = (name) => {
    let group = found.get(name);
    if (!group) {
      group = { name, sources: [], tracks: [], albums: new Set(), targets: [] };
      found.set(name, group);
    }
    return group;
  };

  for (const target of state.summary.targets) {
    const name = targetName(target);
    ensure(name).targets.push(target);

    for (const source of target.sources) {
      // Where this name would go if it is kept ticked, and where it goes now.
      const proposed = state.moved.get(source.value) || name;
      const under = state.detached.has(source.value) ? source.value : proposed;
      nameOf.set(source.value, under);
      ensure(under).sources.push({ ...source, proposed });
    }
  }

  for (const track of state.tracks) {
    const seen = new Set();
    for (const raw of [track.artist, track.albumArtist]) {
      if (!raw) continue;
      const name = nameOf.get(raw) || raw;
      if (seen.has(name)) continue;
      seen.add(name);

      const group = ensure(name);
      group.tracks.push(track);
      if (track.album) group.albums.add(track.album);
    }
  }

  return [...found.values()].sort((a, b) =>
    b.tracks.length - a.tracks.length || a.name.localeCompare(b.name));
}

let groups = [];
// Which artists are expanded, by name, so a redraw keeps them open.
const expanded = new Set();

function renderArtists() {
  if (!state.summary) return;

  groups = buildGroups();
  $("artists-from").textContent = state.summary.rawArtists;
  $("artists-to").textContent = groups.length;
  $("count-artists").textContent = groups.length || "";
  renderArtistNames();
  renderMergeSummary();

  const needle = $("artist-search").value.trim().toLowerCase();
  const list = $("artist-list");
  list.replaceChildren();

  for (const group of groups) {
    if (needle) {
      const haystack = [group.name, ...group.sources.map((s) => s.value)].join(" ").toLowerCase();
      if (!haystack.includes(needle)) continue;
    }
    list.append(artistRow(group));
  }
}

/* The whole reduction on one screen ---------------------------------------- */

$("summary-toggle").addEventListener("click", () => {
  const panel = $("merge-summary");
  panel.hidden = !panel.hidden;
  $("summary-toggle").textContent = panel.hidden ? "Details" : "Hide";
  if (!panel.hidden) renderMergeSummary();
});

// renderMergeSummary lists every name that changes and what it becomes, so the
// whole reduction can be checked and corrected in one place.
//
// The rows are grouped by the name they are headed for rather than by where
// they ended up: a row that has been unticked stays under the same heading,
// unticked, instead of vanishing the moment it is turned off.
function renderMergeSummary() {
  const panel = $("merge-summary");
  if (panel.hidden) return;

  panel.replaceChildren();

  const byTarget = new Map();
  for (const group of groups) {
    for (const source of group.sources) {
      if (source.value === source.proposed) continue;
      if (!byTarget.has(source.proposed)) byTarget.set(source.proposed, []);
      byTarget.get(source.proposed).push(source);
    }
  }

  if (!byTarget.size) {
    panel.append(el("div", "row-sub", "Nothing is being merged."));
    return;
  }

  const blocks = [...byTarget.entries()].sort((a, b) =>
    b[1].length - a[1].length || a[0].localeCompare(b[0]));

  for (const [name, sources] of blocks) panel.append(summaryGroup(name, sources));
}

// summaryGroup is one heading — the artist these names become — and the names
// themselves. The heading is a text field: typing another artist there sends
// the ticked names to that artist instead, which is how a handful of "Future,
// Juice Wrld" credits end up under Juice WRLD without moving Future itself.
function summaryGroup(name, sources) {
  const block = el("div", "summary-group");

  const head = el("div", "summary-head");
  const field = el("input", "summary-target");
  field.type = "text";
  field.value = name;
  field.spellcheck = false;
  field.setAttribute("list", "artist-names");
  field.title = "Type another artist to send the ticked names there";

  // Both change and blur fire when a name is typed and the field is left, and
  // the first of them redraws the panel, so the second is ignored.
  let committed = false;
  const commit = () => {
    const wanted = field.value.trim();
    if (committed || !wanted || wanted === name) {
      field.value = name;
      return;
    }
    committed = true;
    retarget(sources, wanted);
  };
  field.addEventListener("keydown", (event) => {
    if (event.key === "Enter") field.blur();
    if (event.key === "Escape") { field.value = name; field.blur(); }
  });
  field.addEventListener("change", commit);
  field.addEventListener("blur", commit);

  const ticked = sources.filter((source) => !state.detached.has(source.value)).length;
  head.append(field, el("span", "count", `${ticked} of ${plural(sources.length, "name")}`));
  block.append(head);

  for (const source of sources) block.append(summaryRow(source, name));
  return block;
}

function summaryRow(source, name) {
  const row = el("label", "summary-row");
  if (state.detached.has(source.value)) row.classList.add("off");

  const box = el("input");
  box.type = "checkbox";
  box.checked = !state.detached.has(source.value);
  box.addEventListener("change", () => {
    if (box.checked) state.detached.delete(source.value);
    else state.detached.add(source.value);
    renderArtists();
    refreshPending();
  });

  row.append(
    box,
    el("span", "from", source.value),
    el("span", "arrow", "→"),
    el("span", "to", name),
    el("span", "count", tracks(source.tracks)),
  );
  return row;
}

// retarget sends the ticked names of one block to another artist. Names that
// were turned off stay where they were, so unticking is not undone by it.
function retarget(sources, wanted) {
  for (const source of sources) {
    if (state.detached.has(source.value)) continue;
    if (wanted === source.value) state.moved.delete(source.value);
    else state.moved.set(source.value, wanted);
  }
  renderArtists();
  refreshPending();
}

// The list behind the heading field, so an artist can be picked rather than
// typed out.
function renderArtistNames() {
  const list = $("artist-names");
  list.replaceChildren();
  for (const group of groups) {
    const option = document.createElement("option");
    option.value = group.name;
    list.append(option);
  }
}

/* One artist --------------------------------------------------------------- */

function artistRow(group) {
  const folded = group.sources.filter((s) => s.value !== group.name);
  const block = el("div", "group");
  const head = el("div", "group-head");

  const main = el("div", "row-main");
  const name = el("div", "row-name");
  name.append(el("b", null, group.name));
  main.append(name);

  const parts = [tracks(group.tracks.length)];
  if (group.albums.size) parts.push(plural(group.albums.size, "album"));
  if (folded.length) parts.push(`${folded.length} folded in`);
  main.append(el("div", "row-sub", parts.join(" · ")));

  const details = el("button", "btn small", expanded.has(group.name) ? "Hide" : "Details");
  details.addEventListener("click", () => {
    if (expanded.has(group.name)) expanded.delete(group.name);
    else expanded.add(group.name);
    renderArtists();
  });

  const mergeInto = el("button", "btn small", "Merge into…");
  mergeInto.addEventListener("click", () => openMergeInto(group));

  const rename = el("button", "btn small ghost", "Rename");
  rename.addEventListener("click", () => startRename(block, group));

  head.append(main, details, mergeInto, rename);
  block.append(head);

  if (expanded.has(group.name)) block.append(detailsPanel(group));
  if (folded.length) block.classList.add("changed");
  return block;
}

// detailsPanel lists what folds into this artist, then every album under it
// with its tracks.
function detailsPanel(group) {
  const panel = el("div", "details");

  if (group.sources.length > 1) {
    panel.append(el("div", "details-head", "Names in the files"));
    const variants = el("div", "group-variants");
    for (const source of group.sources) variants.append(sourceRow(group, source));
    panel.append(variants);
  }

  panel.append(el("div", "details-head", plural(group.albums.size || 1, "album")));

  const byAlbum = new Map();
  for (const track of group.tracks) {
    const album = track.album || "No album";
    if (!byAlbum.has(album)) byAlbum.set(album, []);
    byAlbum.get(album).push(track);
  }

  for (const [album, list] of [...byAlbum.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    const block = el("div", "album");
    const title = el("div", "album-head");
    title.append(el("b", null, album), el("span", "count", tracks(list.length)));
    block.append(title);

    list.sort((a, b) => (a.discNo - b.discNo) || (a.trackNo - b.trackNo)
      || String(a.title).localeCompare(String(b.title)));

    for (const track of list) {
      const row = el("div", "album-track");
      row.append(
        el("span", "num", track.trackNo ? String(track.trackNo) : ""),
        el("span", "name", track.title || baseName(track.path)),
        el("span", "count", duration(track.seconds)),
      );
      row.addEventListener("contextmenu", (event) => openTrackMenu(event, track));
      block.append(row);
    }
    panel.append(block);
  }

  return panel;
}

function sourceRow(group, source) {
  const row = el("label", "variant");
  const box = el("input");
  box.type = "checkbox";
  box.checked = !state.detached.has(source.value);
  box.disabled = source.value === group.name;

  box.addEventListener("change", () => {
    if (box.checked) state.detached.delete(source.value);
    else state.detached.add(source.value);
    renderArtists();
    refreshPending();
  });

  row.append(box, el("span", null, source.value));
  row.append(el("span", "count", tracks(source.tracks)));
  return row;
}

// renameGroup points every artist behind a row at a new name. Aiming two of
// them at the same name is what merges them.
function renameGroup(group, value) {
  for (const target of group.targets) {
    if (value && value !== target.name) state.renamed.set(target.name, value);
    else state.renamed.delete(target.name);
  }
  // A detached name keeps its own row, and renaming that row means the user
  // wants it under the new name after all.
  for (const source of group.sources) {
    if (source.value === group.name) state.detached.delete(source.value);
  }

  expanded.delete(group.name);
  renderArtists();
  refreshPending();
}

// startRename swaps the row for a text field, committed with Enter.
function startRename(block, group) {
  const input = el("input", "rename-input");
  input.type = "text";
  input.value = group.name;

  const commit = () => renameGroup(group, input.value.trim());
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") commit();
    if (event.key === "Escape") renderArtists();
  });
  input.addEventListener("blur", commit);

  block.replaceChildren(input);
  input.focus();
  input.select();
}

/* Merging one artist into another ------------------------------------------ */

let mergeSource = null;

$("merge-close").addEventListener("click", () => { $("merge-modal").hidden = true; });
$("merge-search").addEventListener("input", renderMergeOptions);

$("merge-search").addEventListener("keydown", (event) => {
  if (event.key !== "Enter") return;
  const typed = $("merge-search").value.trim();
  if (typed) commitMerge(typed);
});

function openMergeInto(group) {
  mergeSource = group;
  $("merge-title").textContent = `Put ${group.name} under…`;
  $("merge-search").value = "";
  renderMergeOptions();
  $("merge-modal").hidden = false;
  $("merge-search").focus();
}

function renderMergeOptions() {
  const needle = $("merge-search").value.trim().toLowerCase();
  const list = $("merge-options");
  list.replaceChildren();

  for (const group of groups) {
    if (group === mergeSource) continue;
    if (needle && !group.name.toLowerCase().includes(needle)) continue;

    const row = el("div", "row-item");
    const main = el("div", "row-main");
    main.append(el("div", "row-name", group.name), el("div", "row-sub", tracks(group.tracks.length)));
    row.append(main);
    row.addEventListener("click", () => commitMerge(group.name));
    list.append(row);
  }
}

function commitMerge(name) {
  if (mergeSource) renameGroup(mergeSource, name);
  mergeSource = null;
  $("merge-modal").hidden = true;
}

/* Cleanup ------------------------------------------------------------------ */

for (const radio of document.querySelectorAll('input[name="aa-mode"]')) {
  radio.addEventListener("change", () => {
    state.albumArtistMode = radio.value;
    refreshPending();
  });
}

for (const [id, key] of [
  ["rule-compilation", "removeCompilation"],
  ["rule-sort", "removeSort"],
  ["rule-legacy", "removeLegacy"],
  ["rule-artwork", "shrinkArtwork"],
  ["rule-backup", "backup"],
]) {
  $(id).addEventListener("change", (event) => {
    state[key] = event.target.checked;
    refreshPending();
  });
}

function renderCleanup(result) {
  const s = state.summary;
  $("aa-count").textContent = `${s.albumArtistCount} of ${tracks(s.trackCount)} carry one`;
  $("compilation-count").textContent = s.compilationCount
    ? `set on ${tracks(s.compilationCount)}` : "not set anywhere";
  $("sort-count").textContent = s.sortCount
    ? `set on ${tracks(s.sortCount)}` : "not set anywhere";
  $("artwork-count").textContent = s.oversizedCount
    ? `${tracks(s.oversizedCount)} carry tags over 3 MB — a phone reads none of them`
    : "every file's tags are small enough to read";
  $("legacy-count").textContent = s.legacyCount
    ? `${tracks(s.legacyCount)} carry one — a phone reads it instead of the real tags`
    : "no file carries one";

  const failures = result.errors || [];
  const errors = $("scan-errors");
  if (failures.length) {
    errors.hidden = false;
    errors.textContent =
      `Could not read ${files(failures.length)}. ` +
      `For example: ${failures[0].path} — ${failures[0].reason}`;
  } else {
    errors.hidden = true;
  }
}

/* Tracks ------------------------------------------------------------------- */

// The columns share the width available: a minimum of zero lets them shrink
// and ellipsise rather than pushing the table into a horizontal scroll.
const columns = [
  { key: "trackNo", label: "#", width: "44px", value: (t) => t.trackNo || "", numeric: true },
  { key: "title", label: "Title", width: "minmax(0, 2.2fr)", value: (t) => t.title || baseName(t.path) },
  { key: "artist", label: "Artist", width: "minmax(0, 1.5fr)", value: (t) => t.artist },
  { key: "albumArtist", label: "Album artist", width: "minmax(0, 1.3fr)", value: (t) => t.albumArtist },
  { key: "album", label: "Album", width: "minmax(0, 1.5fr)", value: (t) => t.album },
  { key: "genre", label: "Genre", width: "minmax(0, 1fr)", value: (t) => t.genre },
  { key: "year", label: "Year", width: "54px", value: (t) => t.year || "", numeric: true },
  { key: "seconds", label: "Time", width: "54px", value: (t) => duration(t.seconds), numeric: true },
];

const ROW_HEIGHT = 30;
const gridTemplate = () => `30px ${columns.map((c) => c.width).join(" ")}`;
const selection = new Set();

let sortKey = "artist";
let sortAsc = true;
let visible = [];
let lastClicked = -1;

$("track-search").addEventListener("input", renderTracks);
$("track-body").addEventListener("scroll", paintRows);
$("edit-selected").addEventListener("click", openBulkEditor);

function filteredTracks() {
  const needle = $("track-search").value.trim().toLowerCase();
  let rows = state.tracks;

  if (needle) {
    rows = rows.filter((t) =>
      `${t.title} ${t.artist} ${t.albumArtist} ${t.album} ${t.genre}`.toLowerCase().includes(needle));
  }

  const column = columns.find((c) => c.key === sortKey) || columns[1];
  const direction = sortAsc ? 1 : -1;

  return [...rows].sort((a, b) => {
    const left = a[column.key] ?? "";
    const right = b[column.key] ?? "";
    if (column.numeric) return (left - right) * direction;
    return String(left).localeCompare(String(right), undefined, { sensitivity: "base" }) * direction;
  });
}

function renderTracks() {
  if (!state.summary) return;

  visible = filteredTracks();
  renderHead();
  $("track-count").textContent = tracks(visible.length);

  // The list is a fixed-height window over the rows, so a library of several
  // thousand tracks scrolls without putting all of them in the page at once.
  $("track-spacer").style.height = `${visible.length * ROW_HEIGHT}px`;
  paintRows();
  updateSelectionUI();
}

function renderHead() {
  const head = $("track-head");
  head.style.gridTemplateColumns = gridTemplate();
  head.replaceChildren();

  const all = el("input");
  all.type = "checkbox";
  all.checked = visible.length > 0 && visible.every((t) => selection.has(t.path));
  all.addEventListener("change", () => {
    for (const track of visible) {
      if (all.checked) selection.add(track.path);
      else selection.delete(track.path);
    }
    paintRows();
    updateSelectionUI();
  });

  const cell = el("div", "th check");
  cell.append(all);
  head.append(cell);

  for (const column of columns) {
    const header = el("div", "th sortable", column.label);
    if (column.numeric) header.classList.add("num");
    if (column.key === sortKey) header.classList.add(sortAsc ? "asc" : "desc");
    header.addEventListener("click", () => {
      if (sortKey === column.key) sortAsc = !sortAsc;
      else { sortKey = column.key; sortAsc = true; }
      renderTracks();
    });
    head.append(header);
  }
}

function paintRows() {
  const body = $("track-body");
  const rows = $("track-rows");

  const first = Math.max(0, Math.floor(body.scrollTop / ROW_HEIGHT) - 8);
  const count = Math.ceil(body.clientHeight / ROW_HEIGHT) + 16;

  rows.style.transform = `translateY(${first * ROW_HEIGHT}px)`;
  rows.replaceChildren();

  visible.slice(first, first + count)
    .forEach((track, offset) => rows.append(trackRow(track, first + offset)));
}

function trackRow(track, index) {
  const row = el("div", "tr");
  row.style.gridTemplateColumns = gridTemplate();
  if (selection.has(track.path)) row.classList.add("selected");

  const box = el("input");
  box.type = "checkbox";
  box.checked = selection.has(track.path);
  box.addEventListener("click", (event) => { event.stopPropagation(); toggle(index, event); });

  const cell = el("div", "td check");
  cell.append(box);
  row.append(cell);

  for (const column of columns) {
    const value = el("div", "td", String(column.value(track) ?? ""));
    if (column.numeric) value.classList.add("num");
    row.append(value);
  }

  row.addEventListener("click", (event) => toggle(index, event));
  row.addEventListener("contextmenu", (event) => {
    // Right-clicking outside the selection acts on that row instead.
    if (!selection.has(track.path)) {
      selection.clear();
      selection.add(track.path);
      lastClicked = index;
      paintRows();
      updateSelectionUI();
    }
    openTrackMenu(event, track);
  });
  return row;
}

// toggle follows the conventions of every track list: a plain click replaces
// the selection, a modifier adds one row, and shift takes the range.
function toggle(index, event) {
  const track = visible[index];
  if (!track) return;

  if (event.shiftKey && lastClicked >= 0) {
    const [from, to] = index < lastClicked ? [index, lastClicked] : [lastClicked, index];
    for (let i = from; i <= to; i++) selection.add(visible[i].path);
  } else if (event.ctrlKey || event.metaKey) {
    if (selection.has(track.path)) selection.delete(track.path);
    else selection.add(track.path);
    lastClicked = index;
  } else {
    const onlyThis = selection.size === 1 && selection.has(track.path);
    selection.clear();
    if (!onlyThis) selection.add(track.path);
    lastClicked = index;
  }

  paintRows();
  updateSelectionUI();
}

function updateSelectionUI() {
  $("selected-count").textContent = selection.size ? `${selection.size} selected` : "";
  $("edit-selected").disabled = selection.size === 0;
  renderHead();
}

/* Right-click menu --------------------------------------------------------- */

const menu = $("context-menu");

document.addEventListener("click", () => { menu.hidden = true; });
document.addEventListener("contextmenu", (event) => {
  if (!event.target.closest(".tr, .album-track")) menu.hidden = true;
});
window.addEventListener("blur", () => { menu.hidden = true; });

function openTrackMenu(event, track) {
  event.preventDefault();
  event.stopPropagation();

  const chosen = selection.size > 1 ? `${selection.size} tracks` : "this track";
  const items = [
    {
      label: "Show in file manager",
      disabled: state.onPhone,
      run: async () => {
        try {
          await go().RevealFile(track.path);
        } catch (err) {
          toast(String(err), true);
        }
      },
    },
    { label: "Copy file path", run: () => copyText(track.path) },
    { label: "Copy artist and title", run: () => copyText(`${track.artist} — ${track.title}`) },
    { separator: true },
    {
      label: `Select everything by ${track.artist || "this artist"}`,
      disabled: !track.artist,
      run: () => selectWhere((t) => t.artist === track.artist),
    },
    {
      label: `Select the album ${track.album || ""}`.trim(),
      disabled: !track.album,
      run: () => selectWhere((t) => t.album === track.album),
    },
    { separator: true },
    { label: "Lyrics from a link…", run: () => openLinkLyrics(track) },
    { label: `Edit ${chosen}…`, run: () => { ensureSelected(track); openBulkEditor(); } },
  ];

  menu.replaceChildren();
  for (const item of items) {
    if (item.separator) {
      menu.append(el("div", "menu-separator"));
      continue;
    }
    const button = el("button", "menu-item", item.label);
    button.disabled = Boolean(item.disabled);
    button.addEventListener("click", () => { menu.hidden = true; item.run(); });
    menu.append(button);
  }

  // Shown first so it can be measured, then nudged back inside the window.
  menu.hidden = false;
  const { width, height } = menu.getBoundingClientRect();
  menu.style.left = `${Math.min(event.clientX, window.innerWidth - width - 8)}px`;
  menu.style.top = `${Math.min(event.clientY, window.innerHeight - height - 8)}px`;
}

function ensureSelected(track) {
  if (!selection.has(track.path)) {
    selection.clear();
    selection.add(track.path);
    paintRows();
    updateSelectionUI();
  }
}

function selectWhere(predicate) {
  selection.clear();
  for (const track of state.tracks) {
    if (predicate(track)) selection.add(track.path);
  }
  showView("tracks");
  updateSelectionUI();
  paintRows();
  toast(`${selection.size} selected`);
}

function copyText(text) {
  navigator.clipboard.writeText(text)
    .then(() => toast("Copied"))
    .catch(() => toast("Could not copy", true));
}

/* Bulk editing ------------------------------------------------------------- */

const bulkFields = [
  { key: "artist", label: "Artist" },
  { key: "albumArtist", label: "Album artist" },
  { key: "album", label: "Album" },
  { key: "title", label: "Title" },
  { key: "genre", label: "Genre" },
  { key: "year", label: "Year", numeric: true },
  { key: "trackNo", label: "Track number", numeric: true },
  { key: "discNo", label: "Disc number", numeric: true },
];

$("bulk-close").addEventListener("click", () => { $("bulk-modal").hidden = true; });
$("bulk-confirm").addEventListener("click", applyBulkEdit);

function selectedTracks() {
  return state.tracks.filter((t) => selection.has(t.path));
}

function openBulkEditor() {
  const chosen = selectedTracks();
  if (!chosen.length) return;

  $("bulk-title").textContent = `Edit ${tracks(chosen.length)}`;
  const container = $("bulk-fields");
  container.replaceChildren();

  for (const field of bulkFields) {
    const values = new Set(chosen.map((t) => String(t[field.key] ?? "")));
    const shared = values.size === 1 ? [...values][0] : "";

    const wrap = el("label", "bulk-field");
    wrap.append(el("span", null, field.label));

    const input = el("input");
    input.type = field.numeric ? "number" : "text";
    input.dataset.key = field.key;
    input.dataset.original = shared;
    input.value = shared;
    if (values.size > 1) input.placeholder = "(mixed)";

    const box = el("input");
    box.type = "checkbox";
    box.dataset.clear = field.key;
    box.addEventListener("change", () => { input.disabled = box.checked; });

    const clear = el("label", "bulk-clear");
    clear.append(box, el("span", null, "clear"));

    const row = el("div", "row");
    row.append(input, clear);
    wrap.append(row);
    container.append(wrap);
  }

  $("bulk-modal").hidden = false;
}

async function applyBulkEdit() {
  const edit = {};
  let touched = false;

  for (const field of bulkFields) {
    const input = document.querySelector(`#bulk-fields input[data-key="${field.key}"]`);
    const clear = document.querySelector(`#bulk-fields input[data-clear="${field.key}"]`);

    if (clear.checked) {
      edit[field.key] = field.numeric ? 0 : "";
      touched = true;
      continue;
    }
    // A field nobody typed into is left alone, which is what makes it safe to
    // edit a mixed selection one column at a time.
    if (input.value === input.dataset.original) continue;

    edit[field.key] = field.numeric ? Number(input.value) || 0 : input.value.trim();
    touched = true;
  }

  $("bulk-modal").hidden = true;
  if (!touched) return;

  const paths = [...selection];
  try {
    await go().EditTracks(paths, edit);
    $("revert").hidden = false;
    toast(`Queued changes for ${tracks(paths.length)}`);
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
}

$("revert").addEventListener("click", async () => {
  try {
    await go().ClearEdits();
    $("revert").hidden = true;
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
});

/* Lyrics from a link ------------------------------------------------------- */

// Searching cannot place every track: a song filed under a transliteration of
// its artist is unreachable from what the file says. Pasting the page settles
// it, and what came back is shown before anything is staged.
let linkTrack = null;
let linkLyrics = "";

$("link-close").addEventListener("click", () => { $("link-modal").hidden = true; });
$("link-fetch").addEventListener("click", fetchLink);
$("link-url").addEventListener("keydown", (event) => {
  if (event.key === "Enter") fetchLink();
});

function openLinkLyrics(track) {
  linkTrack = track;
  linkLyrics = "";

  $("link-title").textContent = `Lyrics for ${track.title || baseName(track.path)}`;
  $("link-url").value = "";
  $("link-status").textContent = "Paste the song's page from a lyrics site.";
  $("link-preview").hidden = true;
  $("link-preview").textContent = "";
  $("link-confirm").disabled = true;
  $("link-modal").hidden = false;
  $("link-url").focus();
}

async function fetchLink() {
  const link = $("link-url").value.trim();
  if (!link) return;

  $("link-fetch").disabled = true;
  $("link-status").textContent = "Reading the page…";
  try {
    const found = await go().LyricsFromLink(link);
    linkLyrics = found.lyrics;

    const lines = linkLyrics.split("\n").length;
    $("link-status").textContent = `${found.source}: ${plural(lines, "line")}`;
    $("link-preview").textContent = linkLyrics;
    $("link-preview").hidden = false;
    $("link-confirm").disabled = false;
  } catch (err) {
    linkLyrics = "";
    $("link-status").textContent = String(err);
    $("link-preview").hidden = true;
    $("link-confirm").disabled = true;
  } finally {
    $("link-fetch").disabled = false;
  }
}

$("link-confirm").addEventListener("click", async () => {
  if (!linkTrack || !linkLyrics) return;
  try {
    await go().EditTracks([linkTrack.path], { lyrics: linkLyrics });
    $("link-modal").hidden = true;
    toast(`Lyrics staged for ${linkTrack.title || baseName(linkTrack.path)}`);
    refreshPending();
  } catch (err) {
    toast(String(err), true);
  }
});

/* Pending plan ------------------------------------------------------------- */

const refreshPending = debounce(async () => {
  if (!state.summary || state.busy) return;

  try {
    const preview = await go().Preview(rules());
    const count = preview.summary.files;

    $("pending").textContent = count ? `${files(count)} will change` : "No changes yet";
    $("pending").classList.toggle("ready", count > 0);
    $("apply").disabled = count === 0;
    $("preview").disabled = count === 0;
  } catch (err) {
    toast(String(err), true);
  }
}, 120);

/* Preview and apply -------------------------------------------------------- */

$("preview").addEventListener("click", showPreview);
$("modal-close").addEventListener("click", () => { $("modal").hidden = true; });
$("modal-apply").addEventListener("click", () => { $("modal").hidden = true; runApply(); });
$("apply").addEventListener("click", runApply);

async function showPreview() {
  try {
    const preview = await go().Preview(rules());
    const body = $("modal-body");
    body.replaceChildren();

    for (const change of preview.changes || []) {
      const block = el("div", "change");
      block.append(el("div", "change-path", change.path));
      for (const field of change.fields) {
        const line = el("div", "change-field");
        line.append(
          el("span", "label", field.name),
          el("span", "before", field.before || "—"),
          el("span", "arrow", "→"),
          el("span", "after", field.after),
        );
        block.append(line);
      }
      body.append(block);
    }

    const total = preview.summary.files;
    $("modal-title").textContent = `${files(total)} will change`;
    $("modal-note").textContent = preview.shown < total ? `Showing the first ${preview.shown}` : "";
    $("modal").hidden = false;
  } catch (err) {
    toast(String(err), true);
  }
}

async function runApply() {
  state.busy = true;
  $("apply").disabled = true;
  $("preview").disabled = true;
  $("pending").textContent = "Applying…";

  try {
    const report = await go().Apply();
    const failed = report.failed || [];

    let message = `Changed ${files(report.written)}`;
    if (failed.length) message += `, ${failed.length} failed`;
    if (report.cancelled) message += " (stopped)";
    toast(message, failed.length > 0);

    if (failed.length) console.warn("Could not write:", failed);
  } catch (err) {
    toast(String(err), true);
  } finally {
    state.busy = false;
  }

  // The files on disk have changed, so everything is rebuilt from them.
  rescan();
}

window.runtime.EventsOn("apply:progress", (p) => {
  $("pending").textContent = `Applying ${p.done} of ${p.total}`;
});

/* Lyrics ------------------------------------------------------------------- */

// Every track the run reported, in order, with the log line drawn for it. The
// line is kept rather than redrawn, so a link typed into it survives switching
// what the log shows.
const lyricsRows = [];
const lyricsByPath = new Map();
// Which counter the log is narrowed to; empty shows everything.
let lyricsFilter = "";

const lyricsMatches = {
  found: (row) => row.status === "found",
  missing: (row) => row.status === "missing",
  error: (row) => row.status === "error" || row.status === "unwritten",
  synced: (row) => row.status === "found" && row.synced,
};

const showsRow = (row) => !lyricsFilter || lyricsMatches[lyricsFilter](row);

$("lyrics-start").addEventListener("click", runLyrics);
$("lyrics-cancel").addEventListener("click", () => go().Cancel());

$("lyrics-filters").addEventListener("click", (event) => {
  const button = event.target.closest("button[data-filter]");
  if (!button) return;
  lyricsFilter = lyricsFilter === button.dataset.filter ? "" : button.dataset.filter;
  paintLyricsLog();
});

async function runLyrics() {
  lyricsRows.length = 0;
  lyricsByPath.clear();
  lyricsFilter = "";
  paintLyricsLog();

  $("lyrics-bar").style.width = "0";
  $("lyrics-start").disabled = true;
  $("lyrics-cancel").disabled = false;
  $("lyrics-status").textContent = "Searching…";

  try {
    const report = await go().FetchLyrics({
      onlyMissing: $("lyrics-missing").checked,
      backup: $("lyrics-backup").checked,
      preferSynced: $("lyrics-synced").checked,
      artists: [],
    });

    $("lyrics-status").textContent = report.cancelled
      ? `Stopped — found ${report.found} of ${report.total}`
      : `Done — found ${report.found} of ${report.total}`;
  } catch (err) {
    $("lyrics-status").textContent = "Error";
    toast(String(err), true);
  } finally {
    $("lyrics-start").disabled = false;
    $("lyrics-cancel").disabled = true;
  }
}

function paintLyricsCounts() {
  const count = (kind) => lyricsRows.filter(lyricsMatches[kind]).length;
  $("lyrics-found").textContent = count("found");
  $("lyrics-missing-count").textContent = count("missing");
  $("lyrics-failed").textContent = count("error");
  $("lyrics-synced-count").textContent = count("synced");
}

function paintLyricsLog() {
  for (const button of $("lyrics-filters").querySelectorAll("button")) {
    button.classList.toggle("active", button.dataset.filter === lyricsFilter);
  }
  $("lyrics-log").replaceChildren(...lyricsRows.filter(showsRow).map((row) => row.node));
  paintLyricsCounts();
}

// drawLyricsRow fills a row's line for what is now known about the track.
function drawLyricsRow(row) {
  const label = `${row.artist} — ${row.title}`;
  const node = row.node;
  node.replaceChildren();

  switch (row.status) {
    case "found":
      node.className = "found";
      node.textContent = `✓ ${label}  [${row.source}]${row.synced ? "  · timed" : ""}`;
      return;
    case "unwritten":
      node.className = "error";
      node.textContent = `✗ ${label} — found, but not written: ${row.detail}`;
      return;
    case "missing":
      node.className = "missing";
      node.append(el("div", "", `· ${label} — not found${row.detail ? ` (${row.detail})` : ""}`));
      break;
    default:
      node.className = "error";
      node.append(el("div", "", `✗ ${label} — ${row.detail}`));
  }
  node.append(linkControls(row));
}

// linkControls is the place on a line the search could not fill where the
// song's page can be pasted instead. What comes back is shown, and only
// staged — like any other edit, it is written from the footer.
function linkControls(row) {
  const box = el("div", "link-box");
  const line = el("div", "link-row");
  const input = el("input", "search");
  input.type = "url";
  input.spellcheck = false;
  input.placeholder = "Song page on Genius, AZLyrics, Musixmatch…";
  const fetchButton = el("button", "btn small", "Fetch");
  const status = el("span", "muted");
  const use = el("button", "btn small primary", "Use these lyrics");
  use.hidden = true;
  const preview = el("div", "lyrics-preview compact");
  preview.hidden = true;
  line.append(input, fetchButton, status, use);
  box.append(line, preview);

  let text = "";

  const fetchPage = async () => {
    const link = input.value.trim();
    if (!link) return;
    fetchButton.disabled = true;
    use.hidden = true;
    status.textContent = "Reading the page…";
    try {
      const found = await go().LyricsFromLink(link);
      text = found.lyrics;
      status.textContent = `${found.source}: ${plural(text.split("\n").length, "line")}`;
      preview.textContent = text;
      preview.hidden = false;
      use.hidden = false;
    } catch (err) {
      text = "";
      status.textContent = String(err);
      preview.hidden = true;
    } finally {
      fetchButton.disabled = false;
    }
  };

  fetchButton.addEventListener("click", fetchPage);
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") fetchPage();
  });
  use.addEventListener("click", async () => {
    if (!text) return;
    try {
      await go().EditTracks([row.path], { lyrics: text });
      input.disabled = fetchButton.disabled = true;
      use.hidden = true;
      preview.hidden = true;
      status.textContent = "Staged — written when you apply";
      refreshPending();
    } catch (err) {
      toast(String(err), true);
    }
  });

  return box;
}

window.runtime.EventsOn("lyrics:progress", (p) => {
  $("lyrics-bar").style.width = p.total ? `${(p.done / p.total) * 100}%` : "0";
  $("lyrics-status").textContent = `Processed ${p.done} of ${p.total}`;
});

// What the run is doing once every track has been looked up; on a phone that
// takes long enough to look like a hang otherwise.
window.runtime.EventsOn("lyrics:phase", (text) => {
  $("lyrics-status").textContent = text;
});

window.runtime.EventsOn("lyrics:track", (result) => {
  // A track that was found and then failed to write comes back a second
  // time, and its line is corrected rather than added again.
  let row = lyricsByPath.get(result.path);
  if (row && result.status === "unwritten") {
    row.status = "unwritten";
    row.detail = result.detail;
  } else {
    row = { ...result, node: el("div") };
    lyricsRows.push(row);
    if (result.path) lyricsByPath.set(result.path, row);
  }
  drawLyricsRow(row);

  if (!showsRow(row)) {
    row.node.remove();
  } else if (!row.node.isConnected) {
    const log = $("lyrics-log");
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
    log.append(row.node);
    if (atBottom) log.scrollTop = log.scrollHeight;
  }
  paintLyricsCounts();
});

// A folder passed on the command line, or dropped onto the application, opens
// without waiting for the picker.
window.runtime.EventsOn("open:folder", (folder) => startScan(folder));
