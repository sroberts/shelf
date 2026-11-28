import { test } from 'node:test';
import assert from 'node:assert/strict';
import { quote, buildQuery, readLibraryParams, writeLibraryParams, isFiltered, SORTS } from '../src/lib/query.js';

test('quote leaves plain values alone and quotes the rest', () => {
  assert.equal(quote('merrow'), 'merrow');
  assert.equal(quote('Ada Merrow'), '"Ada Merrow"');
  assert.equal(quote('a:b'), '"a:b"');
  assert.equal(quote('say "hi"'), `'say "hi"'`);
  assert.equal(quote(`O'Brien "Bob"`), `"O'Brien Bob"`);
});

test('buildQuery joins text and filters', () => {
  assert.equal(buildQuery('', {}), '');
  assert.equal(buildQuery(' orbit ', {}), 'orbit');
  assert.equal(buildQuery('orbit', { author: 'Ada Merrow', tag: 'queue', format: 'epub' }), 'orbit author:"Ada Merrow" tag:queue format:epub');
  // genre is not part of the query syntax; it is its own parameter
  assert.equal(buildQuery('', { genre: 'mystery' }), '');
});

test('library params round-trip through the URL', () => {
  const state = { q: 'orbit', author: 'Ada Merrow', series: '', tag: '', format: '', genre: 'mystery', sort: 'added', view: 'list' };
  const search = writeLibraryParams(state);
  assert.equal(search, '?q=orbit&author=Ada+Merrow&genre=mystery&sort=added&view=list');
  assert.deepEqual(readLibraryParams(search), state);
  assert.equal(writeLibraryParams({}), '');
});

test('isFiltered ignores sort and view', () => {
  assert.equal(isFiltered(readLibraryParams('?sort=added&view=list')), false);
  assert.equal(isFiltered(readLibraryParams('?tag=queue')), true);
  assert.equal(isFiltered(readLibraryParams('?genre=mystery')), true);
});

test('the default sort is labelled as the order the server returns', () => {
  // GET /v1/books with no sort orders by author then series (library
  // defaultOrder); a "Title" label there would misstate the list.
  const def = SORTS.find((s) => s.value === '');
  assert.equal(def.label, 'Author');
  assert.ok(SORTS.some((s) => s.value === 'title'), 'title is still a choice');
});
