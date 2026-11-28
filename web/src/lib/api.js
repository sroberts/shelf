// The /v1 client. The UI uses only what any API client could: it has no
// private endpoints, so the web UI, the CLI, MCP and the REST API cannot
// drift apart.

export class ApiError extends Error {
  /**
   * @param {number} status HTTP status, or 0 when shelf could not be reached
   * @param {any} body the decoded JSON body, if there was one
   * @param {string} [fallback] message to use when the body has none
   */
  constructor(status, body, fallback) {
    super(messageFor(status, body, fallback));
    this.status = status;
    this.body = body;
  }
}

/** The message a person should read for a failed request. */
export function messageFor(status, body, fallback) {
  if (body && typeof body.error === 'string' && body.error) return capitalise(body.error);
  if (fallback) return fallback;
  switch (status) {
    case 0:
      return 'Shelf isn’t answering. Check the connection and try again.';
    case 401:
      return 'Your sign-in has ended.';
    case 404:
      return 'That isn’t in the library.';
    case 413:
      return 'That is too large to upload in one go.';
    default:
      return `Something went wrong (${status}).`;
  }
}

const capitalise = (s) => s.charAt(0).toUpperCase() + s.slice(1);

let signedOut = () => {};

/** Called when a request says the sign-in has ended. */
export function onSignedOut(fn) {
  signedOut = fn;
}

/** Builds a path with a query string, leaving out empty values. */
export function withQuery(path, params = {}) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

const seg = (s) => encodeURIComponent(s);

/**
 * One request. Resolves to the decoded JSON, or null for 204.
 * @param {string} method
 * @param {string} path
 * @param {{body?: any}} [opts]
 */
export async function request(method, path, { body } = {}) {
  const headers = { accept: 'application/json' };
  let payload;
  if (body !== undefined) {
    payload = JSON.stringify(body);
    headers['content-type'] = 'application/json';
  }
  let res;
  try {
    // shelf never redirects an API call, so a redirect is the Outpost gateway
    // sending an expired session to its sign-in page.
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin', redirect: 'manual' });
  } catch {
    throw new ApiError(0, null);
  }
  if (res.type === 'opaqueredirect' || res.status === 401) {
    signedOut();
    throw new ApiError(401, null, 'Your sign-in has ended. Sign in again to carry on.');
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(res.status, data);
  return data;
}

/**
 * Uploads files as multipart form data, reporting progress as a fraction.
 * XHR rather than fetch, because fetch reports no upload progress.
 * @param {File[]} files
 * @param {(fraction: number) => void} [onProgress]
 * @returns {Promise<{imported: any[], failed: {name: string, error: string}[]}>}
 */
export function upload(files, onProgress) {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    for (const f of files) form.append('file', f, f.name);
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/v1/books');
    xhr.withCredentials = true;
    xhr.setRequestHeader('accept', 'application/json');
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total);
    };
    xhr.onerror = () => reject(new ApiError(0, null));
    xhr.onload = () => {
      let data = null;
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        // not JSON: a proxy's error page, or the gateway's
      }
      if (xhr.status === 401) {
        signedOut();
        reject(new ApiError(401, null, 'Your sign-in has ended. Sign in again to carry on.'));
      } else if (xhr.status >= 200 && xhr.status < 300) {
        resolve({ imported: data?.imported ?? [], failed: data?.failed ?? [] });
      } else {
        reject(new ApiError(xhr.status, data));
      }
    };
    xhr.send(form);
  });
}

export const coverUrl = (id) => `/v1/books/${id}/cover`;
export const fileUrl = (id) => `/v1/books/${id}/file`;

export const api = {
  me: () => request('GET', '/v1/me'),
  stats: () => request('GET', '/v1/stats'),

  books: (params) => request('GET', withQuery('/v1/books', params)),
  book: (id, user) => request('GET', withQuery(`/v1/books/${id}`, { user })),
  updateBook: (id, patch) => request('PATCH', `/v1/books/${id}`, { body: patch }),

  authors: async () => (await request('GET', '/v1/authors')).authors ?? [],
  series: async () => (await request('GET', '/v1/series')).series ?? [],
  tags: async () => (await request('GET', '/v1/tags')).tags ?? [],
  genres: async () => (await request('GET', '/v1/genres')).genres ?? [],

  shelves: async () => (await request('GET', '/v1/shelves')).shelves ?? [],
  shelf: (name, user) => request('GET', withQuery(`/v1/shelves/${seg(name)}`, { user })),
  createShelf: (name, query) => request('POST', '/v1/shelves', { body: query ? { name, query } : { name } }),
  deleteShelf: (name) => request('DELETE', `/v1/shelves/${seg(name)}`),
  addToShelf: (name, ids) => request('POST', `/v1/shelves/${seg(name)}/books`, { body: { ids } }),
  removeFromShelf: (name, id) => request('DELETE', `/v1/shelves/${seg(name)}/books/${id}`),

  progress: (user) => request('GET', withQuery('/v1/progress', { user })),
  clearProgress: (id, user) => request('DELETE', withQuery(`/v1/progress/${id}`, { user })),

  accounts: async () => (await request('GET', '/v1/accounts')).accounts ?? [],
  createAccount: (name) => request('POST', '/v1/accounts', { body: { name } }),
  resetPassword: (name) => request('POST', `/v1/accounts/${seg(name)}/password`, { body: {} }),
  deleteAccount: (name) => request('DELETE', `/v1/accounts/${seg(name)}`),

  devices: async () => (await request('GET', '/v1/devices')).devices ?? [],
  addDevice: (device) => request('POST', '/v1/devices', { body: device }),
  sync: (name, opts) => request('POST', `/v1/devices/${seg(name)}/sync`, { body: opts }),
  jobs: async () => (await request('GET', '/v1/jobs')).jobs ?? [],
  job: (id) => request('GET', `/v1/jobs/${seg(id)}`),
  cancelJob: (id) => request('DELETE', `/v1/jobs/${seg(id)}`),
};
