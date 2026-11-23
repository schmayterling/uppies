// Behaviour for the admin pages only. Status pages load no script.

// The sidebar state is applied before the page paints, so it does not flash.
if (localStorage.getItem("uppies-sidebar") === "hidden") {
  document.documentElement.dataset.sidebar = "hidden";
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
    const hidden = document.documentElement.dataset.sidebar !== "hidden";
    document.documentElement.dataset.sidebar = hidden ? "hidden" : "shown";
    localStorage.setItem("uppies-sidebar", hidden ? "hidden" : "shown");
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
