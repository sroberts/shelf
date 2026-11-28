import { test } from 'node:test';
import assert from 'node:assert/strict';
import { bytes, relativeTime, year, authors, seriesLabel, percent, genreLabel, readingState, coverPalette, splitList, middle } from '../src/lib/format.js';

test('bytes', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(1023), '1023 B');
  assert.equal(bytes(1536), '1.5 KB');
  assert.equal(bytes(2048), '2 KB');
  assert.equal(bytes(5 * 1024 * 1024), '5 MB');
  assert.equal(bytes(150 * 1024 * 1024), '150 MB');
  assert.equal(bytes(-1), '');
  assert.equal(bytes(NaN), '');
});

test('relativeTime', () => {
  const now = 1_800_000_000_000;
  const s = now / 1000;
  assert.equal(relativeTime(0, now), '');
  assert.equal(relativeTime(s - 10, now), 'just now');
  assert.equal(relativeTime(s - 60, now), '1 minute ago');
  assert.equal(relativeTime(s - 5 * 60, now), '5 minutes ago');
  assert.equal(relativeTime(s - 3 * 3600, now), '3 hours ago');
  assert.equal(relativeTime(s - 26 * 3600, now), 'yesterday');
  assert.equal(relativeTime(s - 3 * 86400, now), '3 days ago');
  assert.match(relativeTime(s - 30 * 86400, now), /\d{4}/);
});

test('year', () => {
  assert.equal(year('2020-03-01'), '2020');
  assert.equal(year('1999'), '1999');
  assert.equal(year(''), '');
  assert.equal(year(undefined), '');
});

test('authors', () => {
  assert.equal(authors([]), 'Unknown author');
  assert.equal(authors(undefined), 'Unknown author');
  assert.equal(authors(['A']), 'A');
  assert.equal(authors(['A', 'B']), 'A and B');
  assert.equal(authors(['A', 'B', 'C']), 'A, B and C');
});

test('seriesLabel', () => {
  assert.equal(seriesLabel('', 2), '');
  assert.equal(seriesLabel('Orbit', 0), 'Orbit');
  assert.equal(seriesLabel('Orbit', 2), 'Orbit #2');
  assert.equal(seriesLabel('Orbit', 2.5), 'Orbit #2.5');
});

test('percent and reading state', () => {
  assert.equal(percent(undefined), '');
  assert.equal(percent(0), '0%');
  assert.equal(percent(0.4), '<1%');
  assert.equal(percent(42.6), '43%');
  assert.equal(percent(140), '100%');
  assert.equal(readingState(undefined), 'unread');
  assert.equal(readingState(40), 'reading');
  assert.equal(readingState(99.7), 'finished');
});

test('genreLabel', () => {
  assert.equal(genreLabel('science-fiction'), 'Science fiction');
  assert.equal(genreLabel(''), '');
});

test('coverPalette is stable per title and differs between titles', () => {
  assert.deepEqual(coverPalette('The Long Orbit'), coverPalette('The Long Orbit'));
  const seen = new Set(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h'].map((t) => coverPalette(`Book ${t}`).background));
  assert.ok(seen.size > 2, 'palette should vary');
});

test('splitList', () => {
  assert.deepEqual(splitList(' a, b ,, c '), ['a', 'b', 'c']);
  assert.deepEqual(splitList(''), []);
});

test('middle shortens from the middle', () => {
  assert.equal(middle('short'), 'short');
  const m = middle('/Volumes/READER/Books/Some Very Long Author Name/Book.epub', 20);
  assert.equal(m.length, 20);
  assert.ok(m.startsWith('/Volumes') && m.endsWith('Book.epub') && m.includes('…'));
});
