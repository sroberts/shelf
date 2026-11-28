// Formatting shared by every screen. Pure functions, tested in test/.

/** 1536 → "1.5 KB". Decimal-free under 10 KB would read oddly, so one place. */
export function bytes(n) {
  if (!Number.isFinite(n) || n < 0) return '';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1).replace(/\.0$/, '')} ${units[i]}`;
}

/** Unix seconds → "just now", "5 minutes ago", "yesterday", "3 Mar 2024". */
export function relativeTime(unix, now = Date.now()) {
  if (!unix) return '';
  const secs = Math.round(now / 1000 - unix);
  if (secs < 45) return 'just now';
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} minute${mins === 1 ? '' : 's'} ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`;
  const days = Math.round(hours / 24);
  if (days === 1) return 'yesterday';
  if (days < 7) return `${days} days ago`;
  return dateLabel(unix);
}

/** Unix seconds → "3 Mar 2024", in the reader's own locale. */
export function dateLabel(unix) {
  if (!unix) return '';
  return new Date(unix * 1000).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

/** "2020-03-01" or "2020" → "2020"; the year is what a shelf reader scans for. */
export function year(pubdate) {
  const m = /^(\d{4})/.exec(pubdate || '');
  return m ? m[1] : '';
}

/** ["A", "B", "C"] → "A, B and C". */
export function authors(list) {
  const names = (list || []).filter(Boolean);
  if (names.length <= 1) return names[0] || 'Unknown author';
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
}

/** "Earthsea", 2 → "Earthsea #2"; whole numbers lose their ".0". */
export function seriesLabel(series, index) {
  if (!series) return '';
  if (!index) return series;
  return `${series} #${Number.isInteger(index) ? index : String(index)}`;
}

/** 42.37 → "42%"; under 1 but started reads as "<1%". */
export function percent(p) {
  if (p === undefined || p === null || !Number.isFinite(p)) return '';
  if (p > 0 && p < 1) return '<1%';
  return `${Math.round(Math.min(100, Math.max(0, p)))}%`;
}

/** "science-fiction" → "Science fiction". */
export function genreLabel(g) {
  if (!g) return '';
  const s = g.replace(/[-_]+/g, ' ').trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The reading state of a book, from its percent_read. */
export function readingState(p) {
  if (p === undefined || p === null) return 'unread';
  if (p >= 99.5) return 'finished';
  return 'reading';
}

/**
 * Two colours for a placeholder cover, chosen from the title so the same book
 * always gets the same cover. Hues are kept to muted cloth-binding tones.
 */
export function coverPalette(text) {
  let h = 2166136261;
  for (const ch of text || '') {
    h ^= ch.codePointAt(0);
    h = Math.imul(h, 16777619) >>> 0;
  }
  // Deep, muted cloth colours: covers are the only colour on screen, so a
  // stand-in still looks like a book. Lightness stays low enough that the
  // near-white type passes 4.5:1 on every hue.
  // Mix the bits (murmur's finaliser) so similar titles land far apart.
  h ^= h >>> 16;
  h = Math.imul(h, 0x85ebca6b) >>> 0;
  h ^= h >>> 13;
  h = Math.imul(h, 0xc2b2ae35) >>> 0;
  h = (h ^ (h >>> 16)) >>> 0;
  // Sixteen hues and three layouts: two coverless books rarely look alike,
  // and when their hues do collide the type still sits differently.
  const hues = [0, 20, 38, 55, 96, 130, 152, 174, 190, 206, 222, 238, 256, 276, 300, 330];
  const hue = hues[h % hues.length];
  const light = 21 + ((h >>> 8) % 10);
  const layout = ['foot', 'head', 'band'][(h >>> 16) % 3];
  return { background: `hsl(${hue} 30% ${light}%)`, ink: `hsl(${hue} 12% 97%)`, rule: `hsl(${hue} 18% ${light + 22}%)`, sub: `hsl(${hue} 14% 86%)`, layout };
}

/** Comma-separated text → a trimmed list with empties dropped. */
export function splitList(text) {
  return (text || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

/** Shortens a long path from the middle, where the least telling part is. */
export function middle(text, max = 42) {
  const s = String(text || '');
  if (s.length <= max) return s;
  const keep = max - 1;
  const head = Math.ceil(keep / 2);
  return `${s.slice(0, head)}…${s.slice(s.length - (keep - head))}`;
}
