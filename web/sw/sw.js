// The Shelf service worker. It keeps the app shell — index.html, this build's
// assets, the icons and the manifest — so the app opens fast and installs as
// an app. That is all it keeps. Everything under /v1 (the library, covers,
// downloads), the catalog, MCP, kosync and the healthcheck always go to the
// network: a cached book list would lie about what is on disk, and a cached
// download would be a second copy of a book nobody asked for.
//
// A template: build/pwa.js fills in BUILD and PRECACHE at build time.

const BUILD = '__SHELF_BUILD__';
const PRECACHE = __SHELF_PRECACHE__;
const SHELL = `shelf-shell-${BUILD}`;
const BYPASS = /^\/(v1|opds|mcp|up|users|syncs|\.outpost)(\/|$)/;

// Every file must come from this build: a redirect or a page from the Outpost
// gateway (its sign-in, say) fails the install, and the old worker stays.
self.addEventListener('install', (e) => {
  e.waitUntil(
    (async () => {
      const cache = await caches.open(SHELL);
      try {
        await Promise.all(PRECACHE.map((url) => precache(cache, url)));
      } catch (err) {
        await caches.delete(SHELL);
        throw err;
      }
      await self.skipWaiting();
    })(),
  );
});

async function precache(cache, url) {
  const res = await fetch(new Request(url, { cache: 'reload', redirect: 'manual', credentials: 'same-origin' }));
  if (!res.ok || res.type !== 'basic') throw new Error(`precache ${url}: ${res.status || res.type}`);
  if (url === '/') {
    const body = await res.clone().text();
    if (!body.includes(`name="shelf-build" content="${BUILD}"`)) throw new Error('precache /: not this build');
  }
  await cache.put(url, res);
}

self.addEventListener('activate', (e) => {
  e.waitUntil(
    (async () => {
      const old = (await caches.keys()).filter((k) => k.startsWith('shelf-shell-') && k !== SHELL);
      await Promise.all(old.map((k) => caches.delete(k)));
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || BYPASS.test(url.pathname)) return;
  if (req.mode === 'navigate') {
    e.respondWith(navigate(req));
  } else if (url.pathname.startsWith('/assets/') || PRECACHE.includes(url.pathname)) {
    e.respondWith(caches.match(url.pathname).then((hit) => hit ?? fetch(req)));
  }
});

// Pages come from the network first, so a new build and a lapsed sign-in are
// both seen at once; the cached shell answers only when the network cannot.
async function navigate(req) {
  try {
    return await fetch(req);
  } catch (err) {
    const hit = await caches.match('/', { cacheName: SHELL });
    if (hit) return hit;
    throw err;
  }
}
