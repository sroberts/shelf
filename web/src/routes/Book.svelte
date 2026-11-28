<script>
  import { tick } from 'svelte';
  import { Download, Pencil, ArrowLeft, Plus, X } from '@lucide/svelte';
  import { api, fileUrl } from '../lib/api.js';
  import { session, feature, toast, refreshStats } from '../lib/session.svelte.js';
  import { authors, seriesLabel, bytes, year, percent, relativeTime, dateLabel, genreLabel, splitList } from '../lib/format.js';
  import { shelfPath } from '../lib/routes.js';
  import { writeLibraryParams } from '../lib/query.js';
  import Cover from '../components/Cover.svelte';
  import ProgressBar from '../components/ProgressBar.svelte';
  import Confirm from '../components/Confirm.svelte';
  import Loading from '../components/Loading.svelte';
  import Empty from '../components/Empty.svelte';

  /** @type {{id: number}} */
  let { id } = $props();

  let book = $state(null);
  let error = $state('');
  let status = $state(0);
  let manual = $state([]); // manual shelves: { name, has }
  let addTo = $state('');
  let confirmClear = $state(false);
  let editing = $state(false);
  let saving = $state(false);
  let form = $state(/** @type {any} */ ({}));
  let editDialog = $state();

  async function load() {
    try {
      book = await api.book(id);
      document.title = `${book.title} · shelf`;
    } catch (err) {
      error = err.message;
      status = err.status;
    }
  }

  // Which manual shelves hold this book. Each manual shelf is read in full;
  // a personal library has few enough shelves that this is cheap.
  async function loadShelves() {
    try {
      const all = (await api.shelves()).filter((s) => s.kind === 'manual');
      const details = await Promise.all(all.map((s) => api.shelf(s.name).catch(() => null)));
      manual = all.map((s, i) => ({ name: s.name, has: Boolean(details[i]?.contents?.some((b) => b.id === id)) }));
    } catch {
      manual = [];
    }
  }

  $effect(() => {
    load();
    loadShelves();
  });

  async function addToShelf() {
    if (!addTo) return;
    try {
      await api.addToShelf(addTo, [id]);
      toast(`Added to ${addTo}.`);
      addTo = '';
      loadShelves();
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function removeFromShelf(name) {
    try {
      await api.removeFromShelf(name, id);
      toast(`Taken off ${name}.`);
      loadShelves();
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function clearProgress() {
    try {
      await api.clearProgress(id);
      toast('Reading position cleared.');
      await load();
    } catch (err) {
      toast(err.message, 'error');
      throw err;
    }
  }

  async function startEdit() {
    form = {
      title: book.title || '',
      authors: (book.authors || []).join(', '),
      series: book.series || '',
      series_index: book.series_index ? String(book.series_index) : '',
      language: book.language || '',
      publisher: book.publisher || '',
      pubdate: book.pubdate || '',
      tags: (book.tags || []).join(', '),
      description: book.description || '',
    };
    editing = true;
    editDialog.showModal();
    // The form renders after the dialog opens, so autofocus has nothing to
    // act on yet; put the cursor in Title once it exists.
    await tick();
    editDialog.querySelector('input')?.focus();
  }

  /** Only what changed is sent, so a field shelf could not read is left alone. */
  function patchFromForm() {
    const p = {};
    const text = ['title', 'series', 'language', 'publisher', 'pubdate', 'description'];
    for (const k of text) {
      if (form[k].trim() !== (book[k] || '')) p[k] = form[k].trim();
    }
    const list = (v) => splitList(v).join('\u0000');
    if (list(form.authors) !== (book.authors || []).join('\u0000')) p.authors = splitList(form.authors);
    if (list(form.tags) !== (book.tags || []).join('\u0000')) p.tags = splitList(form.tags);
    const idx = form.series_index.trim() === '' ? 0 : Number(form.series_index);
    if (idx !== (book.series_index || 0)) p.series_index = idx;
    return p;
  }

  async function save(e) {
    e.preventDefault();
    if (form.series_index.trim() !== '' && !Number.isFinite(Number(form.series_index))) {
      toast('The series number must be a number, like 2 or 2.5.', 'error');
      return;
    }
    const patch = patchFromForm();
    if (!Object.keys(patch).length) {
      editDialog.close();
      return;
    }
    saving = true;
    try {
      book = await api.updateBook(id, patch);
      document.title = `${book.title} · shelf`;
      toast('Saved to the book’s file.');
      editDialog.close();
      refreshStats();
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      saving = false;
    }
  }

  const tagHref = (t) => '/' + writeLibraryParams({ tag: t });
  const authorHref = (a) => '/' + writeLibraryParams({ author: a });
  const seriesHref = (s) => '/' + writeLibraryParams({ series: s, sort: 'series' });
  const reading = $derived(book?.reading ?? (book?.percent_read !== undefined ? { percent: book.percent_read } : null));
  const canEdit = $derived(book?.format === 'epub');

  // Identifier schemes as people know them; anything else is shown as an
  // identifier with its scheme beside it, rather than as a bare label.
  const knownIds = { isbn: 'ISBN', uuid: 'UUID', asin: 'ASIN', doi: 'DOI', calibre: 'Calibre id', google: 'Google Books' };
  const idLabel = (scheme) => knownIds[scheme] || 'Identifier';
</script>

<a class="back muted small" href="/" onclick={(e) => { if (history.length > 1) { e.preventDefault(); history.back(); } }}>
  <ArrowLeft size={16} /> Library
</a>

{#if error}
  <Empty title={status === 404 ? 'No such book' : 'Couldn’t open this book'}>
    <p>{error}</p>
  </Empty>
{:else if !book}
  <Loading />
{:else}
  <article class="book">
    <div class="cover-col">
      <div class="plinth"><div class="book-cover"><Cover {book} eager /></div></div>
    </div>

    <div class="info">
      <header>
        <h1>{book.title}</h1>
        <p class="by">
          {#each book.authors?.length ? book.authors : [] as a, i (a)}{#if i}{i === book.authors.length - 1 ? ' and ' : ', '}{/if}<a href={authorHref(a)}>{a}</a>{:else}<span class="muted">{authors([])}</span>{/each}{#if book.series}<span class="sep" aria-hidden="true">·</span><a class="series" href={seriesHref(book.series)}>{seriesLabel(book.series, book.series_index)}</a>{/if}
        </p>
        <p class="facts muted small">
          <span class="fmt">{book.format.toUpperCase()}</span>
          <span>{bytes(book.size)}</span>
          {#if year(book.pubdate)}<span>{year(book.pubdate)}</span>{/if}
          {#if book.language}<span>{book.language}</span>{/if}
          {#if feature('ai') && book.genre}<span class="genre">{genreLabel(book.genre)}</span>{/if}
        </p>
      </header>

      <div class="actions">
        <a class="btn primary large" href={fileUrl(id)} download><Download size={17} /> Download</a>
        {#if canEdit}
          <button class="btn large" onclick={startEdit}><Pencil size={16} /> Edit details</button>
        {/if}
      </div>

      {#if feature('progress') || reading}
        <section class="panel reading" aria-labelledby="reading-h">
          <div class="row">
            <h2 id="reading-h" class="label-h">Reading</h2>
            {#if session.me?.progress_user}<span class="muted small">as {session.me.progress_user}</span>{/if}
          </div>
          {#if reading}
            <div class="pct-row">
              <ProgressBar value={reading.percent} />
              <strong>{percent(reading.percent)}</strong>
            </div>
            <p class="muted small">
              {#if reading.device}On {reading.device}{/if}{#if reading.device && reading.updated_unix}{', '}{/if}{#if reading.updated_unix}{reading.device ? 'synced' : 'Synced'} {relativeTime(reading.updated_unix)}{/if}
            </p>
            <button class="btn small ghost danger-text" onclick={() => (confirmClear = true)}>Clear position</button>
          {:else}
            <p class="muted small">Not opened on the reader yet. Positions appear here once the reader syncs progress to shelf.</p>
          {/if}
        </section>
      {/if}

      {#if book.description}
        <section class="description">
          <p>{book.description}</p>
        </section>
      {/if}

      <section class="shelves" aria-labelledby="shelves-h">
        <h2 id="shelves-h" class="label-h">Shelves</h2>
        {#if manual.some((s) => s.has)}
          <ul class="chips">
            {#each manual.filter((s) => s.has) as s (s.name)}
              <li class="chip">
                <a href={shelfPath(s.name)}>{s.name}</a>
                <button aria-label="Take off {s.name}" onclick={() => removeFromShelf(s.name)}><X size={14} /></button>
              </li>
            {/each}
          </ul>
        {/if}
        {#if manual.some((s) => !s.has)}
          <div class="row">
            <select bind:value={addTo} aria-label="Add to a shelf">
              <option value="">Add to a shelf…</option>
              {#each manual.filter((s) => !s.has) as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
            </select>
            <button class="btn" disabled={!addTo} onclick={addToShelf}><Plus size={17} /> Add</button>
          </div>
        {:else if !manual.length}
          <p class="muted small">No hand-picked shelves yet. <a href="/shelves">Make one</a> to collect books for the reader.</p>
        {/if}
      </section>

      <section aria-labelledby="details-h">
        <h2 id="details-h" class="label-h">Details</h2>
        <dl>
          {#if book.publisher}<dt>Publisher</dt><dd>{book.publisher}</dd>{/if}
          {#if book.pubdate}<dt>Published</dt><dd>{book.pubdate}</dd>{/if}
          {#if book.tags?.length}
            <dt>Tags</dt>
            <dd class="tags">{#each book.tags as t (t)}<a href={tagHref(t)}>{t}</a>{/each}</dd>
          {/if}
          {#if book.added_unix}<dt>Added</dt><dd>{dateLabel(book.added_unix)}</dd>{/if}
          <dt>File</dt><dd class="path">{book.path}</dd>
          {#each Object.entries(book.identifiers || {}) as [scheme, value] (scheme)}
            <dt>{idLabel(scheme)}</dt><dd><span class="path">{value}</span>{#if !knownIds[scheme]}{' '}<span class="muted small">({scheme})</span>{/if}</dd>
          {/each}
          <dt>Book id</dt><dd><code>id:{book.id}</code> <span class="muted small">for <code>shelf meta</code> and friends</span></dd>
        </dl>
      </section>
    </div>
  </article>

  <Confirm
    bind:open={confirmClear}
    title="Clear the reading position?"
    body="This forgets the position shelf has stored for this book. The reader keeps its own and sends it again the next time the book is open with sync on, so this is for clearing out a stale entry, not for moving the reader."
    confirmLabel="Clear position"
    danger
    onconfirm={clearProgress}
  />
{/if}

<dialog bind:this={editDialog} class="edit" onclose={() => (editing = false)} aria-labelledby="edit-h">
  {#if editing}
    <form onsubmit={save}>
      <div class="edit-body">
        <h2 id="edit-h">Edit details</h2>
        <p class="muted small">Changes are written into the EPUB itself, so they travel with the file. The reader gets the new version at the next sync, at the same place, keeping its position.</p>
        <label class="field"><span>Title</span><input type="text" bind:value={form.title} required /></label>
        <label class="field"><span>Authors</span><input type="text" bind:value={form.authors} /><small>Separate several with commas.</small></label>
        <div class="two">
          <label class="field"><span>Series</span><input type="text" bind:value={form.series} /></label>
          <label class="field num"><span>Number</span><input type="text" inputmode="decimal" bind:value={form.series_index} /></label>
        </div>
        <div class="two">
          <label class="field"><span>Publisher</span><input type="text" bind:value={form.publisher} /></label>
          <label class="field"><span>Published</span><input type="text" bind:value={form.pubdate} placeholder="2024-03-01" /></label>
        </div>
        <div class="two">
          <label class="field"><span>Tags</span><input type="text" bind:value={form.tags} /><small>Separate with commas.</small></label>
          <label class="field num"><span>Language</span><input type="text" bind:value={form.language} placeholder="en" /></label>
        </div>
        <label class="field"><span>Description</span><textarea bind:value={form.description}></textarea></label>
      </div>
      <div class="edit-actions">
        <button type="button" class="btn" onclick={() => editDialog.close()}>Cancel</button>
        <button class="btn primary" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      </div>
    </form>
  {/if}
</dialog>

<style>
  .back {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    text-decoration: none;
    margin-bottom: 1.5rem;
  }

  .back:hover {
    color: var(--ink);
  }

  .book {
    display: grid;
    grid-template-columns: minmax(16rem, 26rem) minmax(0, 1fr);
    gap: 4rem;
    align-items: start;
  }

  @media (max-width: 880px) {
    .book {
      grid-template-columns: 1fr;
      gap: 2rem;
    }
  }

  .cover-col {
    position: sticky;
    top: calc(var(--bar) + 2rem);
  }

  @media (max-width: 880px) {
    .cover-col {
      position: static;
    }
  }

  /* The signature at its largest: the book on its plinth. */
  .plinth {
    display: grid;
    place-items: center;
    aspect-ratio: 1 / 1;
    padding: 12% 24%;
    background: var(--tile);
    border-radius: var(--radius-tile);
  }

  .book-cover {
    width: 100%;
  }

  @media (max-width: 880px) {
    .plinth {
      aspect-ratio: 4 / 3;
      padding: 8% 32%;
    }
  }

  .info {
    display: grid;
    gap: 2.25rem;
    min-width: 0;
    padding-top: 0.5rem;
  }

  header {
    display: grid;
    gap: 0.5rem;
  }

  .series {
    color: inherit;
    text-decoration: none;
  }

  .sep {
    margin: 0 0.5rem;
    color: var(--muted);
  }

  .by {
    font-size: var(--text-title);
    letter-spacing: -0.01em;
    color: var(--ink-2);
  }

  .by a {
    color: inherit;
    text-decoration: none;
  }

  .by a:hover {
    color: var(--ink);
    text-decoration: underline;
  }

  .facts {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem 0;
    margin-top: 0.25rem;
  }

  .facts span + span::before {
    content: '·';
    margin: 0 0.5rem;
  }

  .fmt {
    font-weight: 550;
  }

  .actions {
    display: flex;
    gap: 0.6rem;
    flex-wrap: wrap;
  }

  .btn.large {
    min-height: 2.75rem;
    padding: 0.5rem 1.35rem;
    font-size: 0.9375rem;
  }

  .label-h {
    font-size: var(--text-body);
    font-weight: 650;
    letter-spacing: -0.01em;
    margin-bottom: 0.75rem;
  }

  .reading {
    display: grid;
    gap: 0.5rem;
    justify-items: start;
  }

  .reading .row {
    width: 100%;
    justify-content: space-between;
  }

  .reading .label-h {
    margin: 0;
  }

  .pct-row {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 1rem;
    align-items: center;
    width: 100%;
    margin-top: 0.25rem;
  }

  .pct-row strong {
    font-size: var(--text-title);
    font-weight: 650;
    font-variant-numeric: tabular-nums;
  }

  .danger-text {
    color: var(--danger);
    padding-left: 0;
  }

  .description p {
    font-size: 1.0625rem;
    line-height: 1.65;
    max-width: 38rem;
    color: var(--ink-2);
    white-space: pre-line;
  }

  .chips {
    list-style: none;
    margin: 0 0 0.75rem;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: 0.15rem;
    background: var(--tile);
    border-radius: 999px;
    padding: 0.2rem 0.25rem 0.2rem 0.85rem;
    font-size: var(--text-meta);
    font-weight: 550;
  }

  .chip a {
    color: var(--ink);
    text-decoration: none;
  }

  .chip button {
    border: 0;
    background: none;
    cursor: pointer;
    color: var(--muted);
    display: grid;
    padding: 0.3rem;
    border-radius: 50%;
  }

  .chip button:hover {
    color: var(--danger);
    background: var(--tile-2);
  }

  .shelves select {
    width: auto;
    min-width: 12rem;
  }

  dl {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    margin: 0;
    font-size: var(--text-meta);
  }

  dt,
  dd {
    padding: 0.6rem 0;
    border-bottom: 1px solid var(--hairline);
  }

  dt {
    color: var(--muted);
    padding-right: 2rem;
  }

  dd {
    margin: 0;
    min-width: 0;
  }

  .path {
    overflow-wrap: anywhere;
    font-family: var(--mono);
    font-size: 0.8125rem;
  }

  .tags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 0.8rem;
  }

  .edit {
    width: min(40rem, calc(100vw - 1.5rem));
    max-height: calc(100dvh - 2rem);
  }

  .edit form {
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - 2rem);
  }

  .edit-body {
    overflow-y: auto;
    padding: 1.5rem 1.5rem 0.5rem;
    display: grid;
    gap: 1rem;
  }

  .two {
    display: grid;
    grid-template-columns: 1fr 9rem;
    gap: 0.8rem;
  }

  @media (max-width: 480px) {
    .two {
      grid-template-columns: 1fr;
    }
  }

  .edit-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.6rem;
    padding: 1rem 1.5rem 1.5rem;
  }
</style>
