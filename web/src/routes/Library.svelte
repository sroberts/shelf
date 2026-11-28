<script>
  import { onMount } from 'svelte';
  import { Search, LayoutGrid, List, Plus, Upload, X, ChevronDown } from '@lucide/svelte';
  import { api, upload } from '../lib/api.js';
  import { route, navigate } from '../lib/router.svelte.js';
  import { session, feature, toast, refreshStats } from '../lib/session.svelte.js';
  import { buildQuery, readLibraryParams, writeLibraryParams, isFiltered, SORTS } from '../lib/query.js';
  import { genreLabel } from '../lib/format.js';
  import BookGrid from '../components/BookGrid.svelte';
  import Empty from '../components/Empty.svelte';
  import Loading from '../components/Loading.svelte';

  const PAGE = 60;
  const ACCEPT = '.epub,.pdf,.txt,.xtc';

  const filters = $derived(readLibraryParams(route.search));
  const view = $derived(filters.view || localStorage.getItem('shelf.view') || 'grid');

  let books = $state([]);
  let more = $state(false);
  let loading = $state(true);
  let error = $state('');
  const initial = readLibraryParams(location.search);
  let text = $state(initial.q);
  let options = $state({ authors: [], series: [], tags: [], genres: [] });
  let sentinel = $state();
  let showTips = $state(false);
  let generation = 0;

  // Uploads
  let picker = $state();
  let dragging = $state(false);
  let uploading = $state(null); // { count, fraction }

  function set(patch, replace = true) {
    navigate('/' + writeLibraryParams({ ...filters, ...patch }), { replace });
  }

  async function load(reset) {
    const gen = reset ? ++generation : generation;
    const offset = reset ? 0 : books.length;
    loading = true;
    error = '';
    try {
      const res = await api.books({
        q: buildQuery(filters.q, filters),
        genre: filters.genre,
        sort: filters.sort,
        limit: PAGE,
        offset,
      });
      if (gen !== generation) return;
      books = reset ? res.books : [...books, ...res.books];
      more = res.more;
    } catch (err) {
      if (gen !== generation) return;
      error = err.message;
      if (reset) books = [];
    } finally {
      if (gen === generation) loading = false;
    }
  }

  // Reload whenever anything that changes the list changes.
  $effect(() => {
    void [filters.q, filters.author, filters.series, filters.tag, filters.format, filters.genre, filters.sort];
    load(true);
  });

  // The text box writes the URL after a pause, so typing is not a search per key.
  let timer;
  function ontype() {
    clearTimeout(timer);
    timer = setTimeout(() => set({ q: text.trim() }), 300);
  }

  async function loadOptions() {
    const [authors, series, tags, genres] = await Promise.all([
      api.authors().catch(() => []),
      api.series().catch(() => []),
      api.tags().catch(() => []),
      feature('ai') ? api.genres().catch(() => []) : [],
    ]);
    options = { authors, series, tags, genres };
  }

  // The filter row is always shown, so its choices load with the page.
  onMount(loadOptions);

  // The bar's search (desktop) and this page's (phone) write the same URL.
  $effect(() => {
    const q = filters.q;
    if (document.activeElement?.id !== 'library-search') text = q;
  });

  // Load the next page as the end of the list comes into view.
  $effect(() => {
    // Reading more and loading here makes the observer start afresh after each
    // page. A new observer reports at once whether the sentinel is in view,
    // so a page too short to push it out of view still loads the next one;
    // a single long-lived observer only reports changes, and would stall.
    if (!sentinel || !more || loading) return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && more && !loading) load(false);
    }, { rootMargin: '600px' });
    io.observe(sentinel);
    return () => io.disconnect();
  });

  function setView(v) {
    localStorage.setItem('shelf.view', v);
    set({ view: v === 'grid' ? '' : v });
  }

  async function send(files) {
    const list = [...files].filter((f) => /\.(epub|pdf|txt|xtc)$/i.test(f.name));
    const skipped = files.length - list.length;
    if (skipped) toast(`${skipped} file${skipped === 1 ? '' : 's'} skipped: shelf takes EPUB, PDF, TXT and XTC.`, 'error');
    if (!list.length) return;
    uploading = { count: list.length, fraction: 0 };
    try {
      const res = await upload(list, (f) => (uploading = { count: list.length, fraction: f }));
      const n = res.imported.length;
      if (n) toast(`Added ${n} book${n === 1 ? '' : 's'} to the library.`);
      for (const f of res.failed) toast(`${f.name}: ${f.error}`, 'error');
      if (n) {
        refreshStats();
        load(true);
        if (n === 1 && !res.failed.length) navigate(`/books/${res.imported[0].id}`);
      }
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      uploading = null;
    }
  }

  // Dropping files anywhere on the page uploads them.
  onMount(() => {
    let depth = 0;
    const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes('Files');
    const enter = (e) => {
      if (!hasFiles(e) || !feature('upload')) return;
      depth++;
      dragging = true;
    };
    const leave = (e) => {
      if (!hasFiles(e)) return;
      depth = Math.max(0, depth - 1);
      if (!depth) dragging = false;
    };
    const over = (e) => {
      if (hasFiles(e)) e.preventDefault();
    };
    const drop = (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      dragging = false;
      if (feature('upload')) send(e.dataTransfer.files);
    };
    addEventListener('dragenter', enter);
    addEventListener('dragleave', leave);
    addEventListener('dragover', over);
    addEventListener('drop', drop);
    return () => {
      // A search typed just before leaving would otherwise fire after, and
      // navigate back to the library from wherever the person went.
      clearTimeout(timer);
      removeEventListener('dragenter', enter);
      removeEventListener('dragleave', leave);
      removeEventListener('dragover', over);
      removeEventListener('drop', drop);
    };
  });

  const activeFilters = $derived(
    [
      ['author', 'Author'],
      ['series', 'Series'],
      ['tag', 'Tag'],
      ['format', 'Format'],
      ['genre', 'Genre'],
    ].filter(([k]) => filters[k]),
  );
</script>

<section class="head">
  <div class="title-row">
    <h1>Library</h1>
    {#if feature('upload')}
      <button class="btn primary" onclick={() => picker.click()} disabled={Boolean(uploading)} aria-label="Add books">
        <Plus size={17} strokeWidth={2.25} /> <span>Add books</span>
      </button>
      <input bind:this={picker} type="file" accept={ACCEPT} multiple hidden onchange={(e) => { send(e.currentTarget.files); e.currentTarget.value = ''; }} />
    {/if}
  </div>
  {#if session.stats}<p class="count">{session.stats.books} {session.stats.books === 1 ? 'book' : 'books'}</p>{/if}

  <form class="search" role="search" onsubmit={(e) => { e.preventDefault(); clearTimeout(timer); set({ q: text.trim() }); }}>
    <label class="box">
      <span class="visually-hidden">Search the library</span>
      <Search size={17} aria-hidden="true" />
      <input id="library-search" type="search" bind:value={text} oninput={ontype} placeholder="Search titles, authors, series" autocomplete="off" spellcheck="false" />
      {#if text}
        <button type="button" class="clear" aria-label="Clear search" onclick={() => { text = ''; set({ q: '' }); }}><X size={14} /></button>
      {/if}
    </label>
  </form>

  <div class="toolbar">
    <div class="filters" role="group" aria-label="Filter the library">
      {#snippet pick(key, label, any, items, show = (v) => v)}
        <label class="pill" class:on={filters[key]}>
          <span class="visually-hidden">{label}</span>
          <select value={filters[key]} onchange={(e) => set({ [key]: e.currentTarget.value })}>
            <option value="">{any}</option>
            {#each items as o (o.value)}<option value={o.value}>{show(o.value)} ({o.count})</option>{/each}
          </select>
          <ChevronDown size={14} aria-hidden="true" />
        </label>
      {/snippet}
      {@render pick('author', 'Author', 'Author', options.authors)}
      {@render pick('series', 'Series', 'Series', options.series)}
      {@render pick('tag', 'Tag', 'Tag', options.tags)}
      {#if feature('ai') && options.genres.length}
        {@render pick('genre', 'Genre', 'Genre', options.genres, genreLabel)}
      {/if}
      {@render pick('format', 'Format', 'Format', Object.keys(session.stats?.formats || { epub: 1, pdf: 1, txt: 1, xtc: 1 }).map((f) => ({ value: f, count: session.stats?.formats?.[f] ?? 0 })), (v) => v.toUpperCase())}
      {#if activeFilters.length}
        <button class="btn ghost small" onclick={() => set({ author: '', series: '', tag: '', format: '', genre: '' })}>Clear filters</button>
      {/if}
    </div>
    <div class="view">
      <button type="button" class="btn ghost small tips-toggle" aria-expanded={showTips} aria-controls="search-tips" onclick={() => (showTips = !showTips)}>
        Search tips <ChevronDown size={14} aria-hidden="true" />
      </button>
      <label class="pill sort">
        <span class="visually-hidden">Sort by</span>
        <select value={filters.sort} onchange={(e) => set({ sort: e.currentTarget.value })}>
          {#each SORTS as s (s.value)}<option value={s.value}>Sort: {s.label}</option>{/each}
        </select>
        <ChevronDown size={14} aria-hidden="true" />
      </label>
      <div class="seg" role="group" aria-label="Layout">
        <button aria-pressed={view === 'grid'} aria-label="Covers" title="Covers" onclick={() => setView('grid')}><LayoutGrid size={16} /></button>
        <button aria-pressed={view === 'list'} aria-label="List" title="List" onclick={() => setView('list')}><List size={16} /></button>
      </div>
    </div>
  </div>
  {#if showTips}
    <p class="tips" id="search-tips">
      Words search titles, authors and more. Narrow with <code>author:merrow</code>, <code>series:orbit</code>,
      <code>tag:queue</code>, <code>format:pdf</code> or <code>lang:en</code>; quote phrases, as in
      <code>author:"le guin"</code>; combine with <code>and</code>, <code>or</code> and <code>not</code>.
      Press <kbd>/</kbd> to search from any page. The same search works in <code>shelf ls</code>.
    </p>
  {/if}
</section>

{#if uploading}
  <div class="uploading" role="status">
    <p class="small">Adding {uploading.count} file{uploading.count === 1 ? '' : 's'}… {Math.round(uploading.fraction * 100)}%</p>
    <div class="bar"><div style:width="{uploading.fraction * 100}%"></div></div>
  </div>
{/if}

{#if error}
  <div class="notice error">{error} <button class="btn small" onclick={() => load(true)}>Try again</button></div>
{/if}

{#if books.length}
  <BookGrid {books} view={view === 'list' ? 'list' : 'grid'} />
  <div bind:this={sentinel} class="sentinel">
    {#if loading}<Loading label="More books" />{:else if more}<button class="btn" onclick={() => load(false)}>Load more</button>{/if}
  </div>
{:else if loading}
  <Loading label="Opening the library" />
{:else if !error}
  {#if isFiltered(filters)}
    <Empty title="Nothing matches">
      <p>No books match this search. <a href="/">Show the whole library</a>.</p>
    </Empty>
  {:else}
    <Empty title="No books yet">
      <p>Drop EPUB, PDF or TXT files anywhere on this page, or use <strong>Add books</strong>. From a terminal, <code>shelf import</code> does the same.</p>
    </Empty>
  {/if}
{/if}


{#if dragging}
  <div class="drop" aria-hidden="true">
    <div class="drop-card">
      <Upload size={32} strokeWidth={1.75} />
      <p class="drop-title">Drop to add to the library</p>
      <p class="small muted">EPUB, PDF, TXT or XTC</p>
    </div>
  </div>
{/if}

<style>
  /* minmax(0, …): a grid track otherwise grows to its widest child's content,
     and the phone's one-row filter strip would widen the whole page instead of
     scrolling inside itself. */
  .head {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1.5rem;
    margin-bottom: 2rem;
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    justify-content: space-between;
  }

  .count {
    margin-top: -1.25rem;
    color: var(--muted);
    font-size: var(--text-body);
    font-variant-numeric: tabular-nums;
  }

  /* On a desktop the bar's search does this job; a phone has no bar search. */
  .search {
    display: none;
  }

  @media (max-width: 720px) {
    .search {
      display: block;
    }
  }

  .box {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0 0.4rem 0 0.85rem;
    border-radius: var(--radius);
    background: var(--tile);
    color: var(--muted);
    min-height: 2.75rem;
  }

  .box:focus-within {
    background: var(--ground);
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .box input {
    border: 0;
    background: transparent;
    outline: none;
    padding: 0.4rem 0;
    color: var(--ink);
    min-height: 0;
  }

  .box input:focus {
    outline: none;
  }

  .box input::-webkit-search-cancel-button {
    display: none;
  }

  .clear {
    display: grid;
    place-items: center;
    width: 1.6rem;
    height: 1.6rem;
    border: 0;
    border-radius: 50%;
    background: var(--tile-2);
    color: var(--ink-2);
    cursor: pointer;
  }

  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem 1.5rem;
    flex-wrap: wrap;
  }

  .filters,
  .view {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  /* A filter is a pill that names itself until it is set, then turns
     near-black: the only accent marks what narrows the list. */
  .pill {
    position: relative;
    display: inline-flex;
    align-items: center;
    border-radius: 999px;
    background: var(--tile);
    color: var(--ink);
    transition: background 0.15s;
  }

  .pill:hover {
    background: var(--tile-2);
  }

  .pill select {
    appearance: none;
    -webkit-appearance: none;
    width: auto;
    field-sizing: content; /* as wide as the chosen option, not the longest */
    max-width: 14rem;
    min-height: 2rem;
    padding: 0.25rem 1.9rem 0.25rem 0.85rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    font-size: var(--text-meta);
    font-weight: 550;
    text-overflow: ellipsis;
    cursor: pointer;
  }

  .pill select:focus {
    outline: none;
  }

  .pill:focus-within {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  .pill :global(svg) {
    position: absolute;
    right: 0.65rem;
    pointer-events: none;
    color: currentColor;
    opacity: 0.6;
  }

  .pill.on {
    background: var(--accent);
    color: var(--accent-ink);
  }

  .pill.on :global(svg) {
    opacity: 0.85;
  }

  .seg {
    display: inline-flex;
    padding: 2px;
    border-radius: 999px;
    background: var(--tile);
  }

  .seg button {
    display: grid;
    place-items: center;
    width: 2.25rem;
    height: 1.75rem;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
    transition:
      background 0.2s var(--ease-out),
      color 0.15s;
  }

  .seg button[aria-pressed='true'] {
    background: var(--ground);
    color: var(--ink);
  }

  /* A phone gets one row of filters that scrolls sideways, not a wall. */
  @media (max-width: 720px) {
    .toolbar {
      flex-wrap: nowrap;
      flex-direction: column;
      align-items: stretch;
    }

    .filters {
      min-width: 0;
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
      margin: -4px calc(-1 * max(1rem, env(safe-area-inset-left)));
      padding: 4px max(1rem, env(safe-area-inset-left));
    }

    .filters::-webkit-scrollbar {
      display: none;
    }

    /* The strip fades at its right edge, so a pill cut there reads as "more
       this way" rather than as the end of the row. */
    .filters {
      -webkit-mask-image: linear-gradient(90deg, #000 calc(100% - 2rem), transparent);
      mask-image: linear-gradient(90deg, #000 calc(100% - 2rem), transparent);
    }

    .view .tips-toggle {
      margin-right: auto;
      padding-left: 0;
    }

    .filters > * {
      flex: none;
    }

    .view {
      justify-content: space-between;
    }
  }

  @media (pointer: coarse) {
    .pill select {
      min-height: 2.75rem;
    }

    .seg button {
      width: 2.75rem;
      height: 2.5rem;
    }
  }

  .sentinel {
    display: flex;
    justify-content: center;
    padding: 2rem 0;
  }

  .uploading {
    margin-bottom: 1.5rem;
    display: grid;
    gap: 0.5rem;
    max-width: 28rem;
  }

  .uploading .bar {
    height: 3px;
    background: var(--hairline);
    border-radius: 2px;
    overflow: hidden;
  }

  .uploading .bar div {
    height: 100%;
    background: var(--accent);
    transition: width 0.2s var(--ease-out);
  }

  .notice.error {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .tips-toggle :global(svg) {
    transition: transform 0.2s var(--ease-out);
  }

  .tips-toggle[aria-expanded='true'] :global(svg) {
    transform: rotate(180deg);
  }

  .tips {
    max-width: 46rem;
    color: var(--muted);
    font-size: var(--text-meta);
    line-height: 1.75;
  }

  .drop {
    position: fixed;
    inset: 0;
    z-index: 50;
    display: grid;
    place-items: center;
    background: color-mix(in srgb, var(--ground) 72%, transparent);
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    pointer-events: none;
  }

  .drop-card {
    display: grid;
    justify-items: center;
    gap: 0.35rem;
    padding: 2.75rem 4rem;
    border-radius: var(--radius-tile);
    background: var(--ground);
    box-shadow: var(--float);
    color: var(--ink);
    animation: rise 0.3s var(--ease-out);
  }

  .drop-title {
    margin-top: 0.5rem;
    font-size: var(--text-title);
    font-weight: 650;
    letter-spacing: -0.015em;
  }

  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
  }

  @media (max-width: 560px) {
    .title-row .btn span {
      display: none;
    }

    .title-row .btn {
      width: 2.75rem;
      padding: 0;
    }
  }
</style>
