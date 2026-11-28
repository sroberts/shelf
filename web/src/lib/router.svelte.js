// A small History API router: the location is reactive state, and clicks on
// same-origin links move it without a page load.

import { match } from './routes.js';

export const route = $state({ path: location.pathname, search: location.search, ...match(location.pathname) });

function update() {
  const m = match(location.pathname);
  route.path = location.pathname;
  route.search = location.search;
  route.name = m.name;
  route.params = m.params;
}

/** Moves to href. replace keeps it out of the back history (filter edits). */
export function navigate(href, { replace = false } = {}) {
  const url = new URL(href, location.href);
  if (url.origin !== location.origin) {
    location.href = url.href;
    return;
  }
  const changedPage = url.pathname !== location.pathname;
  if (replace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
  update();
  if (changedPage) window.scrollTo(0, 0);
}

addEventListener('popstate', update);

// Paths the server answers itself; following them must be a real page load.
const SERVER = /^\/(v1|opds|mcp|up|users|syncs|\.outpost)(\/|$)/;

addEventListener('click', (e) => {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const a = /** @type {HTMLElement} */ (e.target).closest?.('a');
  if (!a || a.target || a.hasAttribute('download')) return;
  const url = new URL(a.href, location.href);
  if (url.origin !== location.origin || SERVER.test(url.pathname)) return;
  e.preventDefault();
  navigate(url.href);
});
