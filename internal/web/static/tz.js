// Public pages show times in the visitor's timezone. A page cannot learn it
// without script, so this keeps the browser's zone in a cookie that the
// server reads, and reloads once when the page was drawn in another zone.
(() => {
  let zone;
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return;
  }
  if (!zone || !/^[A-Za-z0-9_+\-/]{1,64}$/.test(zone)) return;
  const cookie = "tz=" + zone;
  const has = () => document.cookie.split("; ").includes(cookie);
  if (has()) return;
  const secure = location.protocol === "https:" ? "; secure" : "";
  document.cookie = cookie + "; path=/; max-age=31536000; samesite=lax" + secure;
  // With cookies blocked, a reload would draw the same page again, forever.
  if (!has()) return;
  const picked = new URLSearchParams(location.search).has("tz");
  if (!picked && document.documentElement.dataset.zone !== zone) location.reload();
})();
