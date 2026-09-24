// Applies the saved theme before first paint (avoids a light/dark flash). Kept as a static
// file rather than an inline script so the Content-Security-Policy can forbid inline JS.
(function () {
  try {
    var t = localStorage.getItem('opshub.theme');
    var dark = t === 'dark' || ((t === null || t === 'system') && window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (dark) document.documentElement.classList.add('dark');
  } catch (e) {
    /* storage unavailable: keep the light default */
  }
})();
