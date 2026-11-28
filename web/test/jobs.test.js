import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isActive, planSummary, jobFraction, stateLabel, scopeLabel, transportLabel, normalizeHost, planItems, deletions } from '../src/lib/jobs.js';

test('isActive', () => {
  assert.equal(isActive({ state: 'planning' }), true);
  assert.equal(isActive({ state: 'running' }), true);
  assert.equal(isActive({ state: 'done' }), false);
  assert.equal(isActive(null), false);
});

test('planSummary', () => {
  assert.equal(planSummary(null), '');
  assert.equal(planSummary({ uploads: 0, unchanged: 0 }), 'Nothing to do.');
  assert.equal(planSummary({ uploads: 0, unchanged: 4 }), 'Everything is already on the reader (4 unchanged).');
  assert.equal(planSummary({ uploads: 1, unchanged: 0 }), '1 book to send.');
  assert.equal(planSummary({ uploads: 3, deletes: 2, unchanged: 10 }), '3 books to send, 2 books to delete; 10 already there.');
});

test('jobFraction', () => {
  assert.equal(jobFraction({}), 0);
  assert.equal(jobFraction({ progress: { index: 1, total: 4, sent: 0, size: 100 } }), 0);
  assert.equal(jobFraction({ progress: { index: 2, total: 4, sent: 50, size: 100 } }), 0.375);
  assert.equal(jobFraction({ progress: { index: 4, total: 4, sent: 100, size: 100 } }), 1);
});

test('labels', () => {
  assert.equal(stateLabel({ state: 'planning', dry_run: true }), 'Previewing');
  assert.equal(stateLabel({ state: 'done', dry_run: true }), 'Preview ready');
  assert.equal(stateLabel({ state: 'done', dry_run: false }), 'Synced');
  assert.equal(scopeLabel({ shelf: '' }), 'Whole library');
  assert.equal(scopeLabel({ shelf: 'Queue' }), 'Queue');
});

test('transportLabel', () => {
  assert.equal(transportLabel({ transport: 'sd' }), 'SD card');
  assert.equal(transportLabel({ transport: 'ws' }), 'Wi-Fi');
  assert.equal(transportLabel({}), 'Wi-Fi');
});

test('normalizeHost tidies a pasted address', () => {
  assert.deepEqual(normalizeHost(' http://192.168.1.40/ '), { host: '192.168.1.40' });
  assert.deepEqual(normalizeHost('https://crosspoint.local:81/files?x=1'), { host: 'crosspoint.local:81' });
  assert.deepEqual(normalizeHost('192.168.1.40'), { host: '192.168.1.40' });
  assert.ok(normalizeHost('').error);
  assert.ok(normalizeHost('my reader').error);
});

test('planItems hides folders and uses plain verbs', () => {
  const { items, folders } = planItems({
    ops: [
      { kind: 'mkdir', path: '/Books/A' },
      { kind: 'upload', path: '/Books/A/Tides of Brass.epub', size: 10 },
      { kind: 'delete', path: '/Books/B/Old.epub' },
    ],
  });
  assert.equal(folders, 1);
  assert.deepEqual(items.map((i) => [i.verb, i.name]), [['Send', 'Tides of Brass'], ['Delete', 'Old']]);
  assert.deepEqual(deletions({ ops: [{ kind: 'delete', path: '/x/Y.epub' }, { kind: 'upload', path: '/z.epub' }] }).map((d) => d.name), ['Y']);
});
