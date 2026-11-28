// What the whole app knows about who is signed in and what shelf can do.

import { api, onSignedOut } from './api.js';

export const session = $state({
  /** @type {any} GET /v1/me, once loaded */
  me: null,
  /** @type {any} GET /v1/stats, refreshed after changes */
  stats: null,
  signedOut: false,
  /** Set when /v1/me itself failed for a reason other than sign-in. */
  error: '',
});

onSignedOut(() => {
  session.signedOut = true;
});

export async function loadSession() {
  try {
    session.me = await api.me();
    session.error = '';
  } catch (err) {
    if (err.status !== 401) session.error = err.message;
  }
  refreshStats();
}

export async function refreshStats() {
  try {
    session.stats = await api.stats();
  } catch {
    // The header simply shows no counts.
  }
}

/** Feature flags, defaulting to "on" for what any shelf has. */
export function feature(name) {
  const f = session.me?.features;
  if (!f) return name === 'upload' || name === 'opds';
  return Boolean(f[name]);
}

export const toasts = $state({ items: [] });
let nextToast = 1;

/** Shows a short message; errors stay until dismissed. */
export function toast(message, kind = 'info') {
  const id = nextToast++;
  toasts.items.push({ id, message, kind });
  if (kind !== 'error') setTimeout(() => dismiss(id), 4000);
}

export function dismiss(id) {
  const i = toasts.items.findIndex((t) => t.id === id);
  if (i >= 0) toasts.items.splice(i, 1);
}
