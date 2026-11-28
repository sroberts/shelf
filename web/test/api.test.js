import { test, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { api, request, withQuery, messageFor, ApiError, onSignedOut } from '../src/lib/api.js';

let calls = [];
function respond(status, body, { type = 'basic', json = true } = {}) {
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return {
      status,
      ok: status >= 200 && status < 300,
      type,
      json: async () => {
        if (!json) throw new Error('not json');
        return body;
      },
    };
  };
}

beforeEach(() => {
  calls = [];
});

test('withQuery drops empty values', () => {
  assert.equal(withQuery('/v1/books', {}), '/v1/books');
  assert.equal(withQuery('/v1/books', { q: 'a b', genre: '', offset: 0, limit: 60, user: undefined }), '/v1/books?q=a+b&offset=0&limit=60');
});

test('messageFor prefers the server message', () => {
  assert.equal(messageFor(400, { error: 'only EPUB metadata can be edited' }), 'Only EPUB metadata can be edited');
  assert.match(messageFor(0, null), /answering/);
  assert.match(messageFor(500, null), /500/);
  assert.equal(messageFor(500, null, 'custom'), 'custom');
});

test('request decodes JSON and sends JSON bodies', async () => {
  respond(200, { name: 'x' });
  const out = await api.createShelf('To read', '');
  assert.deepEqual(out, { name: 'x' });
  assert.equal(calls[0].url, '/v1/shelves');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.headers['content-type'], 'application/json');
  assert.deepEqual(JSON.parse(calls[0].init.body), { name: 'To read' });
  assert.equal(calls[0].init.credentials, 'same-origin');
});

test('shelf names are escaped in paths', async () => {
  respond(200, {});
  await api.shelf('Sci-fi / 100%');
  assert.equal(calls[0].url, '/v1/shelves/Sci-fi%20%2F%20100%25');
});

test('204 resolves to null', async () => {
  respond(204, null, { json: false });
  assert.equal(await api.deleteShelf('x'), null);
});

test('errors carry status, message and body', async () => {
  respond(409, { error: 'a sync is already running', job: 'j1' });
  await assert.rejects(api.sync('x4', { dry_run: true }), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, 409);
    assert.equal(err.body.job, 'j1');
    assert.equal(err.message, 'A sync is already running');
    return true;
  });
});

test('a 401 or a gateway redirect reports the sign-in has ended', async () => {
  let signedOut = 0;
  onSignedOut(() => signedOut++);
  respond(401, { error: 'unauthorized' });
  await assert.rejects(request('GET', '/v1/me'), (err) => err.status === 401);
  respond(0, null, { type: 'opaqueredirect', json: false });
  await assert.rejects(request('GET', '/v1/me'), (err) => err.status === 401);
  assert.equal(signedOut, 2);
  assert.equal(calls[1].init.redirect, 'manual');
});

test('a network failure is status 0', async () => {
  globalThis.fetch = async () => {
    throw new TypeError('failed');
  };
  await assert.rejects(api.stats(), (err) => err.status === 0);
});

test('list endpoints unwrap their key', async () => {
  respond(200, { authors: [{ value: 'A', count: 1 }] });
  assert.deepEqual(await api.authors(), [{ value: 'A', count: 1 }]);
  respond(200, {});
  assert.deepEqual(await api.devices(), []);
});
