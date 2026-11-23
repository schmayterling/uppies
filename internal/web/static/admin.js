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
