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
// so a reload or a shared link opens the same one. A pane that holds a problem
// to fix, or a dialog that must reopen, wins over the address.
function showPane() {
  const panes = [...document.querySelectorAll("[data-pane]")];
  if (panes.length === 0) {
    return;
  }
  const flagged = panes.find((pane) => pane.querySelector("[aria-invalid], .problem, dialog[data-open-on-load]"));
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
document.addEventListener("DOMContentLoaded", showPane);
window.addEventListener("hashchange", showPane);
document.addEventListener("click", (event) => {
  const tab = event.target.closest("[data-tab]");
  if (tab) {
    // Switch without the page jumping to the pane's anchor.
    event.preventDefault();
    history.replaceState(null, "", tab.getAttribute("href"));
    for (const flaggedField of document.querySelectorAll("[data-pane] [aria-invalid]")) {
      flaggedField.removeAttribute("aria-invalid");
    }
    showPane();
  }
});
