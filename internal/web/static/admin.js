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
  // The list sits in the top layer, so it is placed by hand under the button,
  // or above it when there is no room below.
  const at = button.getBoundingClientRect();
  list.style.minInlineSize = at.width + "px";
  list.style.insetInlineStart = at.left + "px";
  list.showPopover();
  const below = window.innerHeight - at.bottom;
  const height = list.getBoundingClientRect().height;
  const gap = 4;
  list.style.insetBlockStart = (below < height + gap && at.top > below ? at.top - height - gap : at.bottom + gap) + "px";
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
  if (event.target.closest(".select-button")) {
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
  const button = event.target.closest(".select-button");
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
