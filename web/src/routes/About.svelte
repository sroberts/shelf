<script>
  import { Copy, LogOut } from '@lucide/svelte';
  import { session, feature, toast, refreshStats } from '../lib/session.svelte.js';
  import { bytes, genreLabel } from '../lib/format.js';

  $effect(() => {
    refreshStats();
  });

  const origin = location.origin;
  const opds = `${origin}/opds`;
  const mcp = `${origin}/mcp`;

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast('Copied.');
    } catch {
      toast('Couldn’t copy; select the address instead.', 'error');
    }
  }

  const stats = $derived(session.stats);
  const me = $derived(session.me);
  const genres = $derived(Object.entries(stats?.genres || {}).sort((a, b) => b[1] - a[1]));
</script>

<h1>About this shelf</h1>

<div class="cols">
  <section>
    <h2>The library</h2>
    {#if stats}
      <dl class="figures">
        <div><dt>Books</dt><dd>{stats.books}</dd></div>
        <div><dt>Authors</dt><dd>{stats.authors}</dd></div>
        <div><dt>Series</dt><dd>{stats.series}</dd></div>
        <div><dt>On disk</dt><dd>{bytes(stats.bytes)}</dd></div>
      </dl>
      {#if stats.formats}
        <p class="muted small">{Object.entries(stats.formats).map(([f, n]) => `${n} ${f.toUpperCase()}`).join(' · ')}</p>
      {/if}
      {#if feature('ai') && genres.length}
        <h3>Genres</h3>
        <p class="small">{#each genres as [g, n], i (g)}{#if i}{', '}{/if}{genreLabel(g)} <span class="muted">({n})</span>{/each}</p>
        <p class="muted small">Chosen by the operator’s AI model, once per book.</p>
      {/if}
    {:else}
      <p class="muted">Counting…</p>
    {/if}
  </section>

  <section>
    <h2>Connect</h2>
    {#if feature('opds')}
      <div class="addr">
        <span class="label">Catalog for the reader (OPDS)</span>
        <div class="copy"><code>{opds}</code><button class="btn icon small" aria-label="Copy the catalog address" onclick={() => copy(opds)}><Copy size={15} /></button></div>
        <p class="muted small">Add it on the reader under OPDS servers, with a shelf account. The reader can’t sign in through Outpost, so it uses the account directly.</p>
      </div>
    {/if}
    <div class="addr">
      <span class="label">MCP, for AI agents</span>
      <div class="copy"><code>{mcp}</code><button class="btn icon small" aria-label="Copy the MCP address" onclick={() => copy(mcp)}><Copy size={15} /></button></div>
    </div>
    <div class="addr">
      <span class="label">REST API</span>
      <p class="small"><a href="/v1/openapi.json">/v1/openapi.json</a> <span class="muted">describes every endpoint this page uses.</span></p>
    </div>
    <p class="muted small">From a terminal, <code>shelf</code> opens the same library; <code>shelf --help</code> lists the commands.</p>
  </section>

  <section>
    <h2>You</h2>
    {#if me}
      <p>Signed in as <strong>{me.user || 'nobody'}</strong> <span class="muted small">({me.auth === 'gateway' ? 'through Outpost' : me.auth === 'basic' ? 'shelf account' : 'open access'})</span></p>
      {#if me.progress_user}<p class="muted small">Reading positions shown are {me.progress_user}’s.</p>{/if}
      <p class="muted small">
        {feature('mail') ? 'Newsletters sent to shelf’s mail addresses arrive in the library under Mail.' : 'Mail import is off.'}
      </p>
      {#if me.logout_url}
        <form method="POST" action={me.logout_url}>
          <button class="btn"><LogOut size={17} /> Sign out of Outpost</button>
        </form>
        <p class="muted small">This ends your Outpost sign-in for every app, not only shelf.</p>
      {/if}
      <p class="muted small version">shelf {me.version || stats?.version || ''}</p>
    {:else if session.error}
      <p class="notice error">{session.error}</p>
    {:else}
      <p class="muted">Loading…</p>
    {/if}
  </section>
</div>

<style>
  h1 {
    margin-bottom: 2.5rem;
  }

  /* One readable column of plain sections, separated by space: the page is a
     reference card, not a dashboard. */
  .cols {
    display: grid;
    gap: 3rem;
    max-width: 44rem;
  }

  section {
    display: grid;
    gap: 0.9rem;
  }

  .figures {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    margin: 0;
    font-size: var(--text-meta);
  }

  /* Each pair is wrapped in a div, so the div is the row. */
  .figures div {
    display: contents;
  }

  /* Short rows separate by space alone; hairlines are kept for the one table
     that needs them, a book's details. */
  .figures dt,
  .figures dd {
    padding: 0.3rem 0;
  }

  .figures dt {
    color: var(--muted);
    padding-right: 2rem;
  }

  .figures dd {
    margin: 0;
    font-weight: 550;
    font-variant-numeric: tabular-nums;
  }

  .addr {
    display: grid;
    gap: 0.35rem;
  }

  .copy {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .copy code {
    flex: 1;
    overflow-wrap: anywhere;
    padding: 0.5rem 0.7rem;
    border-radius: var(--radius-sm);
    background: var(--tile);
  }

  .version {
    margin-top: 0.5rem;
    font-family: var(--mono);
    font-size: var(--text-small);
  }
</style>
