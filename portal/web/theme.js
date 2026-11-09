// Light and dark mode for the portal pages. Loaded in <head>, before the
// stylesheet applies, so a page never flashes the wrong colours. The saved choice
// wins; until there is one the page follows the system setting.
'use strict';

const THEME = (() => {
  const KEY = 'st_theme', root = document.documentElement;
  const dark = window.matchMedia('(prefers-color-scheme: dark)');

  function saved() {
    try { const v = localStorage.getItem(KEY); return v === 'dark' || v === 'light' ? v : ''; } catch (e) { return ''; }
  }
  const apply = mode => { root.dataset.theme = mode; };
  const current = () => root.dataset.theme;
  function toggle() {
    const next = current() === 'dark' ? 'light' : 'dark';
    apply(next);
    try { localStorage.setItem(KEY, next); } catch (e) { /* this visit only */ }
  }

  apply(saved() || (dark.matches ? 'dark' : 'light'));
  dark.addEventListener('change', e => { if (!saved()) apply(e.matches ? 'dark' : 'light'); });
  window.addEventListener('storage', e => { if (e.key === KEY) apply(saved() || (dark.matches ? 'dark' : 'light')); }); // other tabs

  return { current, toggle };
})();
