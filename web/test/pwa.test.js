import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildId, precacheList, stampPage, fillWorker } from '../build/pwa.js';

test('buildId depends on content, not order', () => {
  const a = buildId([['a.js', 'x'], ['b.css', 'y']]);
  assert.equal(a, buildId([['b.css', 'y'], ['a.js', 'x']]));
  assert.notEqual(a, buildId([['a.js', 'x2'], ['b.css', 'y']]));
  assert.match(a, /^[0-9a-f]{12}$/);
});

test('precacheList starts at the page and lists each file once', () => {
  assert.deepEqual(precacheList(['index.html', 'assets/i.js', 'sw.js'], ['icons/icon.svg', 'manifest.webmanifest']), [
    '/',
    '/assets/i.js',
    '/icons/icon.svg',
    '/manifest.webmanifest',
    '/sw.js',
  ]);
});

test('stamping and filling', () => {
  assert.equal(stampPage('<meta content="%SHELF_BUILD%">', 'abc'), '<meta content="abc">');
  const out = fillWorker("const BUILD = '__SHELF_BUILD__';\nconst PRECACHE = __SHELF_PRECACHE__;", 'abc', ['/']);
  assert.equal(out, "const BUILD = 'abc';\nconst PRECACHE = [\"/\"];");
});
