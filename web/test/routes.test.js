import { test } from 'node:test';
import assert from 'node:assert/strict';
import { match, bookPath, shelfPath } from '../src/lib/routes.js';

test('match', () => {
  assert.deepEqual(match('/'), { name: 'library', params: {} });
  assert.deepEqual(match('/books/12'), { name: 'book', params: { id: '12' } });
  assert.deepEqual(match('/books/12/'), { name: 'book', params: { id: '12' } });
  assert.deepEqual(match('/books/abc'), { name: 'notfound', params: {} });
  assert.deepEqual(match('/shelves'), { name: 'shelves', params: {} });
  assert.deepEqual(match('/shelves/To%20read'), { name: 'shelf', params: { name: 'To read' } });
  assert.deepEqual(match('/shelves/100%25'), { name: 'shelf', params: { name: '100%' } });
  assert.deepEqual(match('/shelves/%E0%A4%A'), { name: 'shelf', params: { name: '%E0%A4%A' } });
  assert.deepEqual(match('/reading'), { name: 'reading', params: {} });
  assert.deepEqual(match('/devices'), { name: 'devices', params: {} });
  assert.deepEqual(match('/about'), { name: 'about', params: {} });
  assert.deepEqual(match('/nope'), { name: 'notfound', params: {} });
});

test('paths round-trip', () => {
  assert.equal(bookPath(3), '/books/3');
  const p = shelfPath('Sci-fi / 100%');
  assert.deepEqual(match(p), { name: 'shelf', params: { name: 'Sci-fi / 100%' } });
});
