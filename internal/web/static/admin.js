// Behaviour for the admin pages only. Status pages load no script.

// The sidebar state is applied before the page paints, so it does not flash.
if (localStorage.getItem("uppies-sidebar") === "rail") {
  document.documentElement.dataset.sidebar = "rail";
}

document.addEventListener("click", (event) => {
  const opener = event.target.closest("[data-open-dialog]");
  if (opener) {
    document.getElementById(opener.dataset.openDialog).showModal();
    return;
  }
  if (event.target.closest("[data-close-dialog]")) {
    event.target.closest("dialog").close();
    return;
  }
  if (event.target.closest("[data-toggle-sidebar]")) {
    const rail = document.documentElement.dataset.sidebar !== "rail";
    document.documentElement.dataset.sidebar = rail ? "rail" : "full";
    localStorage.setItem("uppies-sidebar", rail ? "rail" : "full");
    return;
  }
  // A click on the backdrop lands on the dialog element itself.
  if (event.target instanceof HTMLDialogElement) {
    event.target.close();
  }
});

// A form that came back with problems reopens its dialog.
document.addEventListener("DOMContentLoaded", () => {
  document.documentElement.classList.add("has-script");
  for (const dialog of document.querySelectorAll("dialog[data-open-on-load]")) {
    dialog.showModal();
  }
});

// Tabs: a page with panes shows one at a time. The tab is kept in the address,
// so a reload or a shared link opens the same one. When the page has just
// loaded, a pane that holds a problem to fix or a dialog that must reopen
// wins over the address. After that, the tabs decide.
function showPane(preferFlagged) {
  const panes = [...document.querySelectorAll("[data-pane]")];
  if (panes.length === 0) {
    return;
  }
  const flagged = preferFlagged && panes.find((pane) => pane.querySelector("[aria-invalid], .problem, dialog[data-open-on-load]"));
  const asked = panes.find((pane) => "#pane-" + pane.dataset.pane === location.hash);
  const active = flagged || asked || panes[0];
  for (const pane of panes) {
    pane.hidden = pane !== active;
  }
  for (const tab of document.querySelectorAll("[data-tab]")) {
    if (tab.dataset.tab === active.dataset.pane) {
      tab.setAttribute("aria-current", "true");
    } else {
      tab.removeAttribute("aria-current");
    }
  }
}
document.addEventListener("DOMContentLoaded", () => showPane(true));
window.addEventListener("hashchange", () => showPane(false));
document.addEventListener("click", (event) => {
  const tab = event.target.closest("[data-tab]");
  if (tab) {
    // Switch without the page jumping to the pane's anchor.
    event.preventDefault();
    history.replaceState(null, "", tab.getAttribute("href"));
    showPane(false);
  }
});

// A link to "#some-dialog" opens that dialog: on arrival from another page,
// and on a click when the dialog is on the page already.
function openDialogFromAddress() {
  if (location.hash.length < 2) {
    return;
  }
  const target = document.getElementById(location.hash.slice(1));
  if (target instanceof HTMLDialogElement && !target.open) {
    target.showModal();
    // The address is cleaned up so that the same link works a second time.
    history.replaceState(null, "", location.pathname + location.search);
  }
}
document.addEventListener("DOMContentLoaded", openDialogFromAddress);
window.addEventListener("hashchange", openDialogFromAddress);
document.addEventListener("click", (event) => {
  const link = event.target.closest("a[href*='#']");
  if (!link) {
    return;
  }
  const url = new URL(link.href);
  const target = url.pathname === location.pathname && document.getElementById(url.hash.slice(1));
  if (target instanceof HTMLDialogElement) {
    event.preventDefault();
    target.showModal();
  }
});

// Selects: the browser's own menu cannot be styled, so each select gets a
// button and a list drawn by the page. The select itself stays in the form,
// out of sight, and still carries the value. Without script it is used as is.
// showUnder opens a popover and places it under its button, or above the
// button when there is no room below. A popover sits in the top layer, where
// no parent can clip it, so it has to be placed by hand.
function showUnder(button, panel) {
  const at = button.getBoundingClientRect();
  panel.showPopover();
  const size = panel.getBoundingClientRect();
  const gap = 4;
  const below = window.innerHeight - at.bottom;
  const top = below < size.height + gap && at.top > below ? at.top - size.height - gap : at.bottom + gap;
  const left = Math.max(gap, Math.min(at.left, window.innerWidth - size.width - gap));
  panel.style.insetBlockStart = Math.max(gap, top) + "px";
  panel.style.insetInlineStart = left + "px";
}

function closeSelect(box) {
  const list = box.querySelector(".select-list");
  if (list.matches(":popover-open")) {
    list.hidePopover();
  }
  box.querySelector(".select-button").setAttribute("aria-expanded", "false");
}

function markOption(box, option) {
  const button = box.querySelector(".select-button");
  for (const other of box.querySelectorAll("[role=option]")) {
    other.classList.toggle("active", other === option);
  }
  button.setAttribute("aria-activedescendant", option.id);
  option.scrollIntoView({ block: "nearest" });
}

function openSelect(box) {
  for (const other of document.querySelectorAll(".select")) {
    if (other !== box) {
      closeSelect(other);
    }
  }
  const button = box.querySelector(".select-button");
  const list = box.querySelector(".select-list");
  list.style.minInlineSize = button.getBoundingClientRect().width + "px";
  showUnder(button, list);
  button.setAttribute("aria-expanded", "true");
  markOption(box, box.querySelector("[aria-selected=true]") || box.querySelector("[role=option]"));
}

function chooseOption(box, option) {
  const select = box.querySelector("select");
  for (const other of box.querySelectorAll("[role=option]")) {
    other.setAttribute("aria-selected", String(other === option));
  }
  box.querySelector(".select-value").textContent = option.textContent;
  closeSelect(box);
  if (select.value !== option.dataset.value) {
    select.value = option.dataset.value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  }
}

function buildSelect(select, number) {
  const box = document.createElement("div");
  box.className = "select";
  const button = document.createElement("button");
  button.type = "button";
  button.className = "select-button";
  button.setAttribute("role", "combobox");
  button.setAttribute("aria-haspopup", "listbox");
  button.setAttribute("aria-expanded", "false");
  const value = document.createElement("span");
  value.className = "select-value";
  button.append(value);
  button.insertAdjacentHTML("beforeend", '<svg class="icon" aria-hidden="true"><use href="/static/icons.svg#chevron-down"/></svg>');
  const list = document.createElement("ul");
  list.className = "select-list";
  list.id = "select-list-" + number;
  list.setAttribute("role", "listbox");
  list.setAttribute("popover", "manual");
  button.setAttribute("aria-controls", list.id);
  [...select.options].forEach((option, index) => {
    const item = document.createElement("li");
    item.id = list.id + "-" + index;
    item.setAttribute("role", "option");
    item.setAttribute("aria-selected", String(option.selected));
    item.dataset.value = option.value;
    item.textContent = option.textContent;
    list.append(item);
    if (option.selected) {
      value.textContent = option.textContent;
    }
  });
  // The label and the hint keep pointing at the control that takes focus.
  if (select.id) {
    button.id = select.id;
    select.removeAttribute("id");
  }
  for (const name of ["aria-describedby", "aria-invalid", "aria-label"]) {
    if (select.hasAttribute(name)) {
      button.setAttribute(name, select.getAttribute(name));
    }
  }
  select.classList.add("select-native");
  select.tabIndex = -1;
  select.setAttribute("aria-hidden", "true");
  select.before(box);
  box.append(select, button, list);
}

document.addEventListener("DOMContentLoaded", () => {
  [...document.querySelectorAll("select")].forEach(buildSelect);
});

document.addEventListener("click", (event) => {
  const box = event.target.closest(".select");
  const option = event.target.closest(".select [role=option]");
  if (option) {
    chooseOption(box, option);
    box.querySelector(".select-button").focus();
    return;
  }
  if (event.target.closest(".select .select-button")) {
    if (box.querySelector(".select-list").matches(":popover-open")) {
      closeSelect(box);
    } else {
      openSelect(box);
    }
    return;
  }
  [...document.querySelectorAll(".select")].forEach(closeSelect);
});

document.addEventListener("keydown", (event) => {
  const button = event.target.closest(".select .select-button");
  if (!button) {
    return;
  }
  const box = button.closest(".select");
  const options = [...box.querySelectorAll("[role=option]")];
  const open = box.querySelector(".select-list").matches(":popover-open");
  const current = options.findIndex((option) => option.classList.contains("active"));
  if (!open) {
    if (["ArrowDown", "ArrowUp", "Enter", " "].includes(event.key)) {
      event.preventDefault();
      openSelect(box);
    }
    return;
  }
  const moves = { ArrowDown: current + 1, ArrowUp: current - 1, Home: 0, End: options.length - 1 };
  if (event.key in moves) {
    event.preventDefault();
    markOption(box, options[Math.max(0, Math.min(options.length - 1, moves[event.key]))]);
  } else if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    chooseOption(box, options[current]);
  } else if (event.key === "Escape") {
    // The key must not also close a dialog the select sits in.
    event.preventDefault();
    closeSelect(box);
  } else if (event.key === "Tab") {
    closeSelect(box);
  } else if (event.key.length === 1) {
    // A letter jumps to the next option that starts with it.
    const letter = event.key.toLowerCase();
    const after = options.slice(current + 1).concat(options.slice(0, current + 1));
    const match = after.find((option) => option.textContent.trim().toLowerCase().startsWith(letter));
    if (match) {
      markOption(box, match);
    }
  }
});

// A placed list would drift away from its button when the page moves under it.
window.addEventListener("resize", () => [...document.querySelectorAll(".select")].forEach(closeSelect));
document.addEventListener("scroll", (event) => {
  if (!(event.target instanceof Element) || !event.target.closest(".select-list")) {
    [...document.querySelectorAll(".select")].forEach(closeSelect);
  }
}, true);

// File fields: the browser's own control cannot be restyled past its button,
// so the field goes out of sight and a button and the file's name stand in
// for it. The field keeps the focus and still carries the file.
function buildFileField(input) {
  const box = document.createElement("span");
  box.className = "file";
  const button = document.createElement("label");
  button.className = "button quiet";
  button.htmlFor = input.id;
  button.textContent = "choose a file";
  const name = document.createElement("span");
  name.className = "file-name";
  name.textContent = "no file chosen";
  input.before(box);
  input.classList.add("file-native");
  box.append(input, button, name);
  input.addEventListener("change", () => {
    name.textContent = input.files.length > 0 ? input.files[0].name : "no file chosen";
    box.classList.toggle("file-chosen", input.files.length > 0);
  });
}

document.addEventListener("DOMContentLoaded", () => {
  [...document.querySelectorAll("input[type=file][id]")].forEach(buildFileField);
});

// Date and time fields: a button that shows the value and a panel with a
// month to pick the day from and two fields for the time. The field itself
// stays in the form, hidden, with the same "2026-11-24T10:00" value the
// browser's own control would send.
const monthNames = ["january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"];
const twoDigits = (number) => String(number).padStart(2, "0");
// Days are handled as UTC dates, so a clock change never skips or repeats one.
const dayKey = (date) => date.getUTCFullYear() + "-" + twoDigits(date.getUTCMonth() + 1) + "-" + twoDigits(date.getUTCDate());
const dayFromKey = (key) => new Date(key + "T00:00:00Z");

function datetimeParts(box) {
  const [day, time] = box.querySelector("input[type=hidden]").value.split("T");
  return { day: day || "", time: time || "12:00" };
}

function setDatetime(box, day, time) {
  const input = box.querySelector("input[type=hidden]");
  const label = box.querySelector(".select-value");
  input.value = day ? day + "T" + time : "";
  label.textContent = day ? day + " " + time : "choose a date and time";
  label.classList.toggle("placeholder", !day);
}

// drawMonth fills the grid with the month in box.dataset.view. One day takes
// part in the tab order: the chosen one, else today, else the first.
function drawMonth(box) {
  const [year, month] = box.dataset.view.split("-").map(Number);
  const chosen = datetimeParts(box).day;
  const today = dayKey(new Date(Date.UTC(new Date().getFullYear(), new Date().getMonth(), new Date().getDate())));
  box.querySelector(".datetime-month").textContent = monthNames[month - 1] + " " + year;
  const body = box.querySelector(".datetime-grid tbody");
  body.replaceChildren();
  const first = new Date(Date.UTC(year, month - 1, 1));
  const count = new Date(Date.UTC(year, month, 0)).getUTCDate();
  let row = document.createElement("tr");
  for (let blank = 0; blank < first.getUTCDay(); blank++) {
    row.append(document.createElement("td"));
  }
  for (let number = 1; number <= count; number++) {
    const key = dayKey(new Date(Date.UTC(year, month - 1, number)));
    const cell = document.createElement("td");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "datetime-day";
    button.dataset.day = key;
    button.textContent = number;
    button.tabIndex = -1;
    button.setAttribute("aria-pressed", String(key === chosen));
    button.setAttribute("aria-label", monthNames[month - 1] + " " + number + ", " + year);
    if (key === today) {
      button.classList.add("today");
    }
    cell.append(button);
    row.append(cell);
    if (row.children.length === 7) {
      body.append(row);
      row = document.createElement("tr");
    }
  }
  if (row.children.length > 0) {
    body.append(row);
  }
  const reachable = body.querySelector("[aria-pressed=true]") || body.querySelector(".today") || body.querySelector(".datetime-day");
  reachable.tabIndex = 0;
}

function closeDatetime(box) {
  const panel = box.querySelector(".datetime-panel");
  if (panel.matches(":popover-open")) {
    panel.hidePopover();
  }
  box.querySelector(".datetime-button").setAttribute("aria-expanded", "false");
}

function openDatetime(box) {
  for (const other of document.querySelectorAll(".datetime")) {
    if (other !== box) {
      closeDatetime(other);
    }
  }
  const parts = datetimeParts(box);
  const shown = parts.day ? dayFromKey(parts.day) : new Date();
  box.dataset.view = parts.day ? parts.day.slice(0, 7) : shown.getFullYear() + "-" + twoDigits(shown.getMonth() + 1);
  const [hour, minute] = parts.time.split(":");
  box.querySelector(".datetime-hour").value = hour;
  box.querySelector(".datetime-minute").value = minute;
  drawMonth(box);
  const button = box.querySelector(".datetime-button");
  showUnder(button, box.querySelector(".datetime-panel"));
  button.setAttribute("aria-expanded", "true");
  box.querySelector(".datetime-day[tabindex='0']").focus();
}

// moveMonth shows the month that lies a number of months from the one shown.
function moveMonth(box, months) {
  const [year, month] = box.dataset.view.split("-").map(Number);
  const target = new Date(Date.UTC(year, month - 1 + months, 1));
  box.dataset.view = target.getUTCFullYear() + "-" + twoDigits(target.getUTCMonth() + 1);
  drawMonth(box);
}

// timeFromFields reads the two time fields, keeping each inside its range.
function timeFromFields(box) {
  const within = (field, most) => twoDigits(Math.max(0, Math.min(most, Number.parseInt(field.value, 10) || 0)));
  return within(box.querySelector(".datetime-hour"), 23) + ":" + within(box.querySelector(".datetime-minute"), 59);
}

function buildDatetime(input, number) {
  const box = document.createElement("div");
  box.className = "datetime";
  const panelId = "datetime-panel-" + number;
  box.innerHTML = `
    <button type="button" class="select-button datetime-button" aria-haspopup="dialog" aria-expanded="false" aria-controls="${panelId}">
      <span class="select-value"></span><svg class="icon" aria-hidden="true"><use href="/static/icons.svg#calendar"/></svg>
    </button>
    <div class="datetime-panel" id="${panelId}" popover="manual" role="dialog" aria-label="choose a date and time">
      <div class="datetime-head">
        <button type="button" class="icon-button" data-month="-1" aria-label="previous month"><svg class="icon" aria-hidden="true"><use href="/static/icons.svg#chevron-left"/></svg></button>
        <span class="datetime-month" aria-live="polite"></span>
        <button type="button" class="icon-button" data-month="1" aria-label="next month"><svg class="icon" aria-hidden="true"><use href="/static/icons.svg#chevron-right"/></svg></button>
      </div>
      <table class="datetime-grid">
        <thead><tr><th abbr="sunday">s</th><th abbr="monday">m</th><th abbr="tuesday">t</th><th abbr="wednesday">w</th><th abbr="thursday">t</th><th abbr="friday">f</th><th abbr="saturday">s</th></tr></thead>
        <tbody></tbody>
      </table>
      <div class="datetime-time">
        <span id="${panelId}-time">time</span>
        <input class="datetime-hour" inputmode="numeric" maxlength="2" aria-label="hour, 0 to 23" aria-describedby="${panelId}-time">
        <span aria-hidden="true">:</span>
        <input class="datetime-minute" inputmode="numeric" maxlength="2" aria-label="minute">
        <button type="button" class="button" data-datetime-done>done</button>
      </div>
    </div>`;
  const button = box.querySelector(".datetime-button");
  // The label and the hint keep pointing at the control that takes focus.
  button.id = input.id;
  input.removeAttribute("id");
  for (const name of ["aria-describedby", "aria-invalid"]) {
    if (input.hasAttribute(name)) {
      button.setAttribute(name, input.getAttribute(name));
    }
  }
  input.before(box);
  input.type = "hidden";
  box.prepend(input);
  const parts = datetimeParts(box);
  setDatetime(box, parts.day, parts.time);
}

document.addEventListener("DOMContentLoaded", () => {
  [...document.querySelectorAll("input[type=datetime-local]")].forEach(buildDatetime);
});

document.addEventListener("click", (event) => {
  const box = event.target.closest(".datetime");
  for (const other of document.querySelectorAll(".datetime")) {
    if (other !== box) {
      closeDatetime(other);
    }
  }
  if (!box) {
    return;
  }
  const day = event.target.closest(".datetime-day");
  const month = event.target.closest("[data-month]");
  if (event.target.closest(".datetime-button")) {
    if (box.querySelector(".datetime-panel").matches(":popover-open")) {
      closeDatetime(box);
    } else {
      openDatetime(box);
    }
  } else if (day) {
    setDatetime(box, day.dataset.day, timeFromFields(box));
    drawMonth(box);
    box.querySelector(".datetime-day[aria-pressed=true]").focus();
  } else if (month) {
    moveMonth(box, Number(month.dataset.month));
  } else if (event.target.closest("[data-datetime-done]")) {
    closeDatetime(box);
    box.querySelector(".datetime-button").focus();
  }
});

// Typing a time changes the value at once when a day is chosen already.
document.addEventListener("input", (event) => {
  const box = event.target.closest(".datetime");
  if (box && event.target.matches(".datetime-hour, .datetime-minute") && datetimeParts(box).day) {
    setDatetime(box, datetimeParts(box).day, timeFromFields(box));
  }
});

// Leaving a time field rewrites it the way it was understood, so 75 minutes reads 59.
document.addEventListener("change", (event) => {
  const box = event.target.closest(".datetime");
  if (box && event.target.matches(".datetime-hour, .datetime-minute")) {
    const [hour, minute] = timeFromFields(box).split(":");
    box.querySelector(".datetime-hour").value = hour;
    box.querySelector(".datetime-minute").value = minute;
  }
});

document.addEventListener("keydown", (event) => {
  const box = event.target.closest(".datetime");
  if (!box || !box.querySelector(".datetime-panel").matches(":popover-open")) {
    return;
  }
  if (event.key === "Escape") {
    // The key must not also close a dialog the field sits in.
    event.preventDefault();
    closeDatetime(box);
    box.querySelector(".datetime-button").focus();
    return;
  }
  const day = event.target.closest(".datetime-day");
  const steps = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 };
  if (!day || !(event.key in steps)) {
    return;
  }
  event.preventDefault();
  const target = dayFromKey(day.dataset.day);
  target.setUTCDate(target.getUTCDate() + steps[event.key]);
  const key = dayKey(target);
  if (key.slice(0, 7) !== box.dataset.view) {
    box.dataset.view = key.slice(0, 7);
    drawMonth(box);
  }
  box.querySelector(`.datetime-day[data-day="${key}"]`).focus();
});

window.addEventListener("resize", () => [...document.querySelectorAll(".datetime")].forEach(closeDatetime));

// Saving without a button: a form marked data-autosave is sent as soon as one
// of its fields changes, which for a text field is when the focus leaves it.
// The server answers a form it refuses with a page that marks the problems,
// so such a form is then sent again the ordinary way to show that page.
let toastTimer;
function toast(text, failed) {
  let note = document.querySelector(".toast");
  if (!note) {
    note = document.createElement("p");
    note.className = "toast";
    note.setAttribute("role", "status");
    document.body.append(note);
  }
  note.textContent = text;
  note.classList.toggle("toast-failed", Boolean(failed));
  note.classList.add("toast-shown");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => note.classList.remove("toast-shown"), 1600);
}

async function saveForm(form) {
  const data = new FormData(form);
  // A form without files is sent the way the browser itself would send it.
  const body = form.enctype === "multipart/form-data" ? data : new URLSearchParams(data);
  let response;
  try {
    response = await fetch(form.action, { method: "POST", body });
  } catch {
    toast("not saved. the server did not answer.", true);
    return false;
  }
  if (!response.ok) {
    HTMLFormElement.prototype.submit.call(form);
    return false;
  }
  toast("saved");
  for (const frame of document.querySelectorAll("iframe[data-preview]")) {
    frame.contentWindow.location.reload();
  }
  return true;
}

document.addEventListener("change", async (event) => {
  const form = event.target.closest("form[data-autosave]");
  if (!form) {
    return;
  }
  const saved = await saveForm(form);
  // A new file, or a monitor added to a list, changes what the page has to show.
  if (saved && (event.target.type === "file" || event.target.name === "add" || event.target.name.startsWith("remove_"))) {
    location.reload();
  }
});

// The list of monitors in a status page's editor: arrows and dragging move a
// row, the cross takes it off the page, and each change is saved at once.
// Without script the same buttons send the form, and the server does the move.
document.addEventListener("click", async (event) => {
  const form = event.target.closest("form[data-editor-list]");
  const row = event.target.closest("[data-row]");
  if (!form || !row) {
    return;
  }
  const move = event.target.closest("[data-move]");
  if (move) {
    event.preventDefault();
    const up = move.dataset.move === "up";
    const neighbour = up ? row.previousElementSibling : row.nextElementSibling;
    if (!neighbour) {
      return;
    }
    if (up) {
      neighbour.before(row);
    } else {
      neighbour.after(row);
    }
    move.focus();
    saveForm(form);
  } else if (event.target.closest("[data-remove]")) {
    event.preventDefault();
    row.remove();
    if (await saveForm(form)) {
      // The monitor goes back into the list of those that can be added.
      location.reload();
    }
  }
});

let draggedRow = null;
document.addEventListener("dragstart", (event) => {
  draggedRow = event.target.closest ? event.target.closest("form[data-editor-list] [data-row]") : null;
  if (draggedRow) {
    event.dataTransfer.effectAllowed = "move";
    draggedRow.classList.add("dragging");
  }
});
document.addEventListener("dragover", (event) => {
  const over = draggedRow && event.target.closest("[data-row]");
  if (!over || over.parentElement !== draggedRow.parentElement) {
    return;
  }
  event.preventDefault();
  if (over === draggedRow) {
    return;
  }
  const box = over.getBoundingClientRect();
  if (event.clientY < box.top + box.height / 2) {
    over.before(draggedRow);
  } else {
    over.after(draggedRow);
  }
});
document.addEventListener("dragend", () => {
  if (!draggedRow) {
    return;
  }
  draggedRow.classList.remove("dragging");
  saveForm(draggedRow.closest("form"));
  draggedRow = null;
});
