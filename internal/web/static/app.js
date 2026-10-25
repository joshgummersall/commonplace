// Vim-style keyboard control. Everything else is htmx attributes in the
// templates.
//
// Selectable things carry [data-item] ("quote", "note", or "theme"); their controls
// carry [data-action], which keys trigger by clicking, so a key and a
// mouse click run the same htmx request. The keymap and help text both
// come from KEYS below.
(() => {
  // ---- selection -------------------------------------------------------

  let current = -1;
  const storeKey = "cmpl:selected:" + location.pathname + location.search;

  // Items inside a closed area on the Themes page are skipped.
  const items = () => [...document.querySelectorAll("main [data-item]")].filter((el) => !el.closest("[hidden]"));

  function paint() {
    items().forEach((el, i) => el.classList.toggle("is-current", i === current));
  }

  function select(i, { scroll = "nearest" } = {}) {
    const list = items();
    // Moving past the last loaded result loads the next page, then moves.
    const more = document.querySelector("[data-load-more]");
    if (i >= list.length && more) {
      if (!more.classList.contains("htmx-request")) more.click();
      message("Loading more…");
      document.addEventListener("htmx:afterSettle", () => select(i, { scroll }), { once: true });
      return;
    }
    if (list.length === 0) return;
    current = Math.max(0, Math.min(i, list.length - 1));
    paint();
    const el = list[current];
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: scroll, behavior: "smooth" });
    try {
      sessionStorage.setItem(storeKey, String(current));
    } catch {}
  }

  // The item to act on: the selected one, or the only one on the page.
  function selected(kind) {
    const list = items();
    let el = current >= 0 ? list[current] : null;
    if (!el) {
      const quotes = list.filter((x) => x.dataset.item === "quote");
      el = list.length === 1 ? list[0] : quotes.length === 1 ? quotes[0] : null;
    }
    if (el && kind && el.dataset.item !== kind) return null;
    return el;
  }

  function act(name, kind) {
    const btn = selected(kind)?.querySelector(`[data-action="${name}"]`);
    if (!btn) return false;
    btn.click();
    return true;
  }

  // ---- status line (mode, pending keys, messages) ----------------------

  const status = document.createElement("div");
  status.className = "statusline";
  status.innerHTML = '<span class="mode"></span><span class="msg"></span><span class="pending"></span>';
  document.body.append(status);
  const [modeEl, msgEl, pendingEl] = status.children;
  let msgTimer;

  function message(text) {
    msgEl.textContent = text;
    clearTimeout(msgTimer);
    msgTimer = setTimeout(() => (msgEl.textContent = ""), 2000);
  }

  const typing = (el) =>
    el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));

  function updateMode() {
    modeEl.textContent = typing(document.activeElement) ? "-- INSERT --" : "";
  }
  document.addEventListener("focusin", updateMode);
  document.addEventListener("focusout", () => setTimeout(updateMode));

  // ---- undo ------------------------------------------------------------

  // Undo entries are recorded from completed requests, so mouse clicks are
  // undoable too. Restores are not themselves recorded (no redo).
  const undoStack = [];
  let undoing = false;

  document.addEventListener("htmx:afterRequest", (e) => {
    const { verb, path } = e.detail.requestConfig;
    if (undoing || !e.detail.successful) return;
    let m;
    if (verb === "post" && (m = path.match(/^\/h\/(\d+)\/(bury|unbury)$/))) {
      const back = m[2] === "bury" ? "unbury" : "bury";
      undoStack.push({ what: m[2], verb: "POST", path: `/h/${m[1]}/${back}`, target: `#h-${m[1]}` });
    } else if (verb === "post" && (m = path.match(/^\/h\/(\d+)\/favorite$/))) {
      undoStack.push({ what: "favorite", verb: "POST", path, target: `#h-${m[1]} .favorite` });
    } else if (verb === "delete" && (m = path.match(/^\/notes\/(\d+)$/))) {
      undoStack.push({ what: "note delete", verb: "POST", path: `/notes/${m[1]}/restore`, target: "#notes" });
    }
  });

  function undo() {
    const u = undoStack.pop();
    if (!u) return message("Already at oldest change");
    if (!document.querySelector(u.target)) return message("Can't undo here");
    undoing = true;
    htmx
      .ajax(u.verb, u.path, { target: u.target, swap: "outerHTML" })
      .then(() => message(`Undid ${u.what}`))
      .finally(() => (undoing = false));
  }

  // ---- folds (Themes page) ---------------------------------------------

  // Open areas are remembered for the session, so coming back from a
  // theme finds the page as it was left.
  const foldKey = "cmpl:open-areas";

  function openAreas() {
    try {
      return new Set(JSON.parse(sessionStorage.getItem(foldKey)) || []);
    } catch {
      return new Set();
    }
  }

  function paintFold(id, open) {
    const list = document.getElementById(`area-${id}-themes`);
    if (!list) return false;
    list.hidden = !open;
    document.querySelector(`#theme-${id} [data-action="fold"]`)?.setAttribute("aria-expanded", String(open));
    return true;
  }

  function applyFolds() {
    const open = openAreas();
    document.querySelectorAll("[data-area]").forEach((el) => paintFold(el.dataset.area, open.has(el.dataset.area)));
  }

  // setFolds opens or closes areas, keeping the same row selected (or
  // its area, if the row was folded away).
  function setFolds(ids, open) {
    const el = current >= 0 ? items()[current] : null;
    const saved = openAreas();
    for (const id of ids) {
      if (paintFold(id, open)) open ? saved.add(id) : saved.delete(id);
    }
    try {
      sessionStorage.setItem(foldKey, JSON.stringify([...saved]));
    } catch {}
    if (!el) return;
    const keep = el.closest("[hidden]") ? el.closest(".area").querySelector("[data-area]") : el;
    select(items().indexOf(keep));
  }

  // The area of the selected row: the row itself, or the area it is in.
  function currentArea() {
    const el = selected("theme");
    return el?.dataset.area ? el : el?.closest(".area")?.querySelector("[data-area]");
  }

  function fold(mode) {
    const area = currentArea();
    const list = area && document.getElementById(`area-${area.dataset.area}-themes`);
    if (!list) return message(area ? "No themes inside" : "Select an area");
    setFolds([area.dataset.area], mode === "toggle" ? list.hidden : mode === "open");
  }

  const foldAll = (open) => setFolds([...document.querySelectorAll("[data-area]")].map((el) => el.dataset.area), open);

  document.addEventListener("click", (e) => {
    const btn = e.target.closest('[data-action="fold"]');
    if (!btn) return;
    const row = btn.closest("[data-area]");
    const list = document.getElementById(`area-${row.dataset.area}-themes`);
    setFolds([row.dataset.area], list.hidden);
  });

  // ---- commands --------------------------------------------------------

  const halfPage = () => {
    const list = items();
    const el = list[Math.max(current, 0)];
    const h = el ? el.getBoundingClientRect().height + 20 : 200;
    return Math.max(1, Math.round(window.innerHeight / 2 / h));
  };

  function open() {
    const link = selected()?.querySelector('[data-action="open"]');
    if (link) link.click();
  }

  // On a quote page, "More like this" scrolls to the panel and selects
  // its first item once loaded.
  function similarPanel() {
    const first = document.querySelector("#similar [data-item]");
    if (!first) return false;
    select(items().indexOf(first), { scroll: "center" });
    return true;
  }

  function writeNote() {
    const box = document.getElementById("write");
    if (box) {
      box.focus();
      return;
    }
    const q = selected("quote");
    if (q) location.href = `/h/${q.dataset.id}#write`;
  }

  // Bury and unbury are one toggle on b.
  function bury() {
    if (!act("bury", "quote")) act("unbury", "quote");
  }

  async function yank() {
    const q = selected();
    if (!q?.querySelector("[data-yank]")) return;
    const text = [...q.querySelectorAll("[data-yank] p")].map((p) => p.innerText.trim()).join("\n\n");
    const cite = q.querySelector("[data-yank-cite]")?.innerText.trim().replace(/\s+/g, " ");
    try {
      await navigator.clipboard.writeText(cite ? `${text}\n— ${cite}` : text);
      message("Yanked");
    } catch {
      message("Clipboard not available");
    }
  }

  function search() {
    const q = document.getElementById("q");
    if (!q) {
      location.href = "/search";
      return;
    }
    q.focus();
    q.select();
  }

  // gf toggles the Favorites filter on Search; elsewhere it opens it.
  function toggleFavorites() {
    const box = document.getElementById("fav");
    if (box) box.click();
    else location.href = "/search?fav=1";
  }

  function toggleHelp(show) {
    const help = document.getElementById("help");
    if (help) help.hidden = show === undefined ? !help.hidden : !show;
  }

  // Each entry: keys, help text, and a function taking the count (1 if
  // none was typed). Sequences such as "gg" and "dd" are matched in full.
  const KEYS = [
    ["Move"],
    [["j"], "Next item", (n) => select(current + n)],
    [["k"], "Previous item", (n) => select(current < 0 ? 0 : current - n)],
    [["n"], "Next result", (n) => select(current + n)],
    [["N"], "Previous result", (n) => select(current < 0 ? 0 : current - n)],
    [["gg"], "First item", () => select(0)],
    [["G"], "Last item", () => select(items().length - 1)],
    [["C-d"], "Half a page down", () => select(Math.max(current, 0) + halfPage())],
    [["C-u"], "Half a page up", () => select(Math.max(current, 0) - halfPage())],
    [["zz"], "Center selection", () => select(Math.max(current, 0), { scroll: "center" })],
    ["Go"],
    [["o", "Enter", "l"], "Open quote and notes, or a theme's highlights", open],
    [["h", "C-o"], "Back", () => history.back()],
    [["gd"], "Daily", () => (location.href = "/")],
    [["gs", "/"], "Search", search],
    [["gf", "F"], "Favorites: toggle the filter on Search", toggleFavorites],
    [["gt"], "Themes", () => (location.href = "/themes")],
    [["t"], "The quote's theme", () => act("theme", "quote") || message("Not in a theme")],
    ["Act"],
    [["f"], "Favorite", () => act("favorite", "quote")],
    [["b"], "Bury / unbury: hide from Daily, keep in Search", bury],
    [["x", "dd"], "Delete selected note", () => act("delete", "note")],
    [["u"], "Undo", undo],
    [["yy"], "Copy quote with source", yank],
    [["s"], "More like this", () => similarPanel() || act("similar", "quote")],
    [["i", "a"], "Write a note", writeNote],
    [["cc"], "Edit selected note, or rename theme", () => act("edit", "note") || act("edit", "theme")],
    ["Themes"],
    [["za"], "Open or close the area", () => fold("toggle")],
    [["zo"], "Open the area", () => fold("open")],
    [["zc"], "Close the area", () => fold("close")],
    [["zR"], "Open every area", () => foldAll(true)],
    [["zM"], "Close every area", () => foldAll(false)],
    [["R"], "Refresh themes", () => document.querySelector('[data-action="refresh"]')?.click()],
    [["?"], "Toggle this help", () => toggleHelp()],
  ];

  const bindings = new Map();
  for (const entry of KEYS) {
    if (entry.length === 1) continue;
    for (const k of entry[0]) bindings.set(k, entry[2]);
  }
  const isPrefix = (seq) => [...bindings.keys()].some((k) => k.length > seq.length && k.startsWith(seq));

  function renderHelp() {
    const help = document.getElementById("help");
    if (!help) return;
    const dl = help.querySelector("dl");
    for (const entry of KEYS) {
      if (entry.length === 1) {
        const h = document.createElement("dt");
        h.className = "group";
        h.textContent = entry[0];
        dl.append(h, document.createElement("dd"));
        continue;
      }
      const dt = document.createElement("dt");
      dt.innerHTML = entry[0].map((k) => `<kbd>${k.replace("C-", "Ctrl-")}</kbd>`).join(" ");
      const dd = document.createElement("dd");
      dd.textContent = entry[1];
      dl.append(dt, dd);
    }
  }

  // ---- key handling ----------------------------------------------------

  let seq = "";
  let count = "";

  function reset() {
    seq = "";
    count = "";
    pendingEl.textContent = "";
  }

  function keyName(e) {
    if (e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1) return "C-" + e.key.toLowerCase();
    if (e.metaKey || e.altKey || e.ctrlKey) return null;
    return e.key;
  }

  document.addEventListener("keydown", (e) => {
    const t = e.target;

    // Insert mode: only Esc and save are ours.
    if (typing(t)) {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter" && t.tagName === "TEXTAREA") {
        e.preventDefault();
        t.form?.requestSubmit();
      } else if (e.key === "Escape") {
        e.preventDefault();
        const cancel = t.form?.querySelector('[data-action="cancel"]');
        if (cancel) cancel.click();
        else t.blur();
      } else if (e.key === "Enter" && t.id === "q") {
        // Like vim's search: Enter moves to the first match.
        e.preventDefault();
        t.form?.requestSubmit();
        t.blur();
        current = -1;
        document.addEventListener("htmx:afterSettle", () => select(0), { once: true });
      }
      return;
    }

    if (e.key === "Escape") {
      reset();
      toggleHelp(false);
      return;
    }
    const key = keyName(e);
    if (!key || key === "Shift") return;
    // Enter on a focused button or link (reached with Tab) keeps its
    // normal meaning.
    if (key === "Enter" && seq === "" && t.closest("button, a")) return;

    if (/^[0-9]$/.test(key) && seq === "" && !(key === "0" && count === "")) {
      count += key;
      pendingEl.textContent = count;
      e.preventDefault();
      return;
    }

    seq += key;
    const fn = bindings.get(seq);
    if (fn) {
      e.preventDefault();
      const n = Math.max(1, parseInt(count || "1", 10));
      reset();
      fn(n);
    } else if (isPrefix(seq)) {
      e.preventDefault();
      pendingEl.textContent = count + seq;
    } else {
      reset();
    }
  });

  // ---- page lifecycle --------------------------------------------------

  // Say when a request fails rather than silently doing nothing.
  document.addEventListener("htmx:responseError", (e) => {
    message(e.detail.xhr.responseText.trim() || `Request failed (${e.detail.xhr.status})`);
  });
  document.addEventListener("htmx:sendError", () => message("Can't reach cmpl serve"));

  // htmx replaces items on favorite, bury, note edits, and renames; keep
  // the selection on the same position.
  document.addEventListener("htmx:afterSettle", (e) => {
    applyFolds();
    // Arriving from "More like this" on another page: select the first
    // similar highlight once the panel has loaded.
    if (e.target.classList?.contains("similar-body") && location.hash === "#similar") similarPanel();
    if (e.target.id === "results") {
      current = -1;
      try {
        sessionStorage.removeItem(storeKey);
      } catch {}
    }
    paint();
    updateMode();
  });

  renderHelp();
  applyFolds();
  if (location.hash === "#write") {
    document.getElementById("write")?.focus();
  } else {
    let saved = null;
    try {
      saved = sessionStorage.getItem(storeKey);
    } catch {}
    if (saved !== null) select(parseInt(saved, 10), { scroll: "center" });
  }
  updateMode();
})();
