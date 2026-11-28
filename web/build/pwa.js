// Builds the service worker from sw/sw.js, with no worker library. At the end
// of a production build it hashes everything the build produced into BUILD,
// stamps index.html with it, and emits sw.js at the root with BUILD and the
// list of files to precache. Dev has no worker.

import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const TEMPLATE = fileURLToPath(new URL('../sw/sw.js', import.meta.url));

/** The build id: the first 12 hex of a sha256 over every file, by name. */
export function buildId(files) {
  const hash = createHash('sha256');
  for (const [name, body] of [...files].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))) {
    hash.update(name).update('\0').update(body).update('\0');
  }
  return hash.digest('hex').slice(0, 12);
}

/** What the worker stores at install: the page, then every other file the
 *  build emitted and everything in public/. */
export function precacheList(bundleNames, publicNames) {
  const files = [...bundleNames.filter((n) => n !== 'index.html'), ...publicNames].map((n) => `/${n}`);
  return ['/', ...[...new Set(files)].sort()];
}

export const stampPage = (html, build) => html.replaceAll('%SHELF_BUILD%', build);

export function fillWorker(template, build, list) {
  return template.replace("'__SHELF_BUILD__'", `'${build}'`).replace('__SHELF_PRECACHE__', JSON.stringify(list));
}

/** Every file under dir, as slash-separated paths relative to it. */
function walk(dir) {
  if (!dir || !existsSync(dir)) return [];
  const out = [];
  for (const name of readdirSync(dir)) {
    if (name.startsWith('.')) continue;
    const full = join(dir, name);
    if (statSync(full).isDirectory()) out.push(...walk(full).map((n) => `${name}/${n}`));
    else out.push(name);
  }
  return out;
}

export function pwa() {
  let publicDir = '';
  return {
    name: 'shelf-pwa',
    apply: 'build',
    configResolved(config) {
      publicDir = config.publicDir;
    },
    generateBundle: {
      // After Vite's own HTML plugin has emitted index.html.
      order: 'post',
      handler(_, bundle) {
        const page = bundle['index.html'];
        if (!page) this.error('shelf-pwa: index.html is not in the bundle');
        const template = readFileSync(TEMPLATE, 'utf8');
        const publicNames = walk(publicDir);
        const files = [
          ...Object.values(bundle).map((f) => [f.fileName, f.type === 'chunk' ? f.code : f.source]),
          ...publicNames.map((n) => [`public/${n}`, readFileSync(join(publicDir, ...n.split('/')))]),
          ['sw/sw.js', template],
        ];
        const build = buildId(files);
        page.source = stampPage(String(page.source), build);
        const list = precacheList(Object.keys(bundle), publicNames);
        this.emitFile({ type: 'asset', fileName: 'sw.js', source: fillWorker(template, build, list) });
      },
    },
  };
}
