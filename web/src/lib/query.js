// Library filters live in the page's URL and become one query in shelf's
// query syntax for GET /v1/books, so a filtered view is a link that can be
// shared or bookmarked, and the same text works in `shelf ls`.

export const FILTERS = ['author', 'series', 'tag', 'format'];

export const SORTS = [
  // '' is the server's default order, which is by author then series: the
  // label must say what the list actually shows.
  { value: '', label: 'Author' },
  { value: 'title', label: 'Title' },
  { value: 'series', label: 'Series' },
  { value: 'added', label: 'Date added' },
  { value: 'modified', label: 'Last changed' },
  { value: 'size', label: 'Size' },
  { value: 'format', label: 'Format' },
];

/**
 * Quotes a value for the query syntax when it needs it. The lexer ends a
 * quoted string at the first matching quote and has no escape, so a value
 * holding double quotes is wrapped in single ones; one holding both loses
 * its double quotes, the only lossy case.
 */
export function quote(value) {
  const v = String(value).trim();
  if (v && !/[\s:()"']/.test(v)) return v;
  if (!v.includes('"')) return `"${v}"`;
  if (!v.includes("'")) return `'${v}'`;
  return `"${v.replaceAll('"', '')}"`;
}

/** The text box plus the active filters, as one query. */
export function buildQuery(text, filters = {}) {
  const parts = [];
  const t = (text || '').trim();
  if (t) parts.push(t);
  for (const f of FILTERS) {
    if (filters[f]) parts.push(`${f}:${quote(filters[f])}`);
  }
  return parts.join(' ');
}

/** Reads the library's state from a URL search string. */
export function readLibraryParams(search) {
  const p = new URLSearchParams(search);
  const out = { q: p.get('q') || '', sort: p.get('sort') || '', genre: p.get('genre') || '', view: p.get('view') || '' };
  for (const f of FILTERS) out[f] = p.get(f) || '';
  return out;
}

/** Writes the library's state back to a search string, leaving out defaults. */
export function writeLibraryParams(state) {
  const p = new URLSearchParams();
  for (const k of ['q', ...FILTERS, 'genre', 'sort', 'view']) {
    if (state[k]) p.set(k, state[k]);
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

/** True when anything narrows the list, so "no books" can say why. */
export function isFiltered(state) {
  return Boolean(state.q || state.genre || FILTERS.some((f) => state[f]));
}
