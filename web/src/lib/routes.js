// Matching paths to screens. Pure, so it is tested without a browser.

/** @typedef {{name: string, params: Record<string, string>}} Match */

/** @type {[string, RegExp][]} */
const TABLE = [
  ['library', /^\/$/],
  ['book', /^\/books\/(?<id>\d+)$/],
  ['shelves', /^\/shelves$/],
  ['shelf', /^\/shelves\/(?<name>[^/]+)$/],
  ['reading', /^\/reading$/],
  ['devices', /^\/devices$/],
  ['about', /^\/about$/],
];

/** @returns {Match} */
export function match(pathname) {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname;
  for (const [name, re] of TABLE) {
    const m = re.exec(path);
    if (m) {
      /** @type {Record<string, string>} */
      const params = {};
      for (const [k, v] of Object.entries(m.groups || {})) {
        try {
          params[k] = decodeURIComponent(v);
        } catch {
          params[k] = v;
        }
      }
      return { name, params };
    }
  }
  return { name: 'notfound', params: {} };
}

export const bookPath = (id) => `/books/${id}`;
export const shelfPath = (name) => `/shelves/${encodeURIComponent(name)}`;
