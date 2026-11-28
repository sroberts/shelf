// Sync job helpers. Pure, so they are tested without a browser.

export const ACTIVE = ['planning', 'running'];

export const isActive = (job) => Boolean(job && ACTIVE.includes(job.state));

/** A one-line account of a plan, for a person deciding whether to run it. */
export function planSummary(plan) {
  if (!plan) return '';
  const parts = [];
  const n = (count, one, many) => (count ? `${count} ${count === 1 ? one : many}` : '');
  parts.push(n(plan.uploads, 'book to send', 'books to send'));
  parts.push(n(plan.moves, 'move', 'moves'));
  parts.push(n(plan.deletes, 'book to delete', 'books to delete'));
  const changes = parts.filter(Boolean);
  if (!changes.length) return plan.unchanged ? `Everything is already on the reader (${plan.unchanged} unchanged).` : 'Nothing to do.';
  const tail = plan.unchanged ? `; ${plan.unchanged} already there` : '';
  return `${changes.join(', ')}${tail}.`;
}

/** How far a running job is, 0–1, counting finished operations and the bytes of the current one. */
export function jobFraction(job) {
  const p = job?.progress;
  if (!p || !p.total) return 0;
  const done = Math.max(0, (p.index || 1) - 1);
  const current = p.size ? Math.min(1, (p.sent || 0) / p.size) : 0;
  return Math.min(1, (done + current) / p.total);
}

/** A short word for a job's state. */
export function stateLabel(job) {
  if (!job) return '';
  switch (job.state) {
    case 'planning':
      return job.dry_run ? 'Previewing' : 'Preparing';
    case 'running':
      return 'Sending';
    case 'done':
      return job.dry_run ? 'Preview ready' : 'Synced';
    case 'failed':
      return job.dry_run ? 'Preview failed' : 'Failed';
    case 'cancelled':
      return 'Cancelled';
    default:
      return job.state;
  }
}

/** The library or the shelf a job sent. */
export const scopeLabel = (job) => (job?.shelf ? job.shelf : 'Whole library');

/** How a reader is connected, in words a person would use. */
export function transportLabel(dev) {
  return dev?.transport === 'sd' || dev?.transport === 'sdcard' ? 'SD card' : 'Wi-Fi';
}

export const isCard = (dev) => transportLabel(dev) === 'SD card';

/**
 * Tidies what someone typed as a reader's address. People paste what their
 * browser shows, so a scheme, a trailing slash or a path is removed rather
 * than refused; what is left must be a bare name or IP address.
 * Returns { host } or { error }.
 */
export function normalizeHost(input) {
  let host = String(input ?? '').trim();
  host = host.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '');
  host = host.replace(/[/?#].*$/, '');
  if (!host) return { error: 'Enter the address the reader shows under File transfer.' };
  if (/\s/.test(host)) return { error: 'An address has no spaces. It looks like 192.168.1.40.' };
  return { host };
}

const verbs = { upload: 'Send', move: 'Move', delete: 'Delete' };

/**
 * The plan as a person reads it: the books it touches, in plain verbs.
 * Folders are counted rather than listed; they follow from the books.
 */
export function planItems(plan) {
  const ops = plan?.ops || [];
  const items = ops
    .filter((op) => op.kind !== 'mkdir')
    .map((op) => ({ ...op, verb: verbs[op.kind] || op.kind, name: bookName(op.path) }));
  return { items, folders: ops.length - items.length };
}

/** The files a prune would delete, as book names, for the confirmation. */
export const deletions = (plan) => planItems(plan).items.filter((op) => op.kind === 'delete');

/** "Ada Merrow/Tides of Brass.epub" → "Tides of Brass". */
export function bookName(path) {
  const base = String(path || '').split('/').pop() || '';
  return base.replace(/\.[a-z0-9]{2,4}$/i, '');
}
