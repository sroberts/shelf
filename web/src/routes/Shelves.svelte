<script>
  import { Plus, Search, Hand } from '@lucide/svelte';
  import { api } from '../lib/api.js';
  import { toast } from '../lib/session.svelte.js';
  import { navigate } from '../lib/router.svelte.js';
  import { shelfPath } from '../lib/routes.js';
  import Loading from '../components/Loading.svelte';
  import Empty from '../components/Empty.svelte';

  let shelves = $state(null);
  let error = $state('');
  let creating = $state(false);
  let name = $state('');
  let kind = $state('manual');
  let query = $state('');
  let busy = $state(false);

  async function load() {
    try {
      shelves = await api.shelves();
      error = '';
    } catch (err) {
      error = err.message;
      shelves = [];
    }
  }

  $effect(() => {
    load();
  });

  async function create(e) {
    e.preventDefault();
    const n = name.trim();
    if (!n) return;
    if (kind === 'query' && !query.trim()) {
      toast('A saved-search shelf needs a search.', 'error');
      return;
    }
    busy = true;
    try {
      await api.createShelf(n, kind === 'query' ? query.trim() : '');
      toast(`Made the shelf ${n}.`);
      navigate(shelfPath(n));
    } catch (err) {
      toast(err.status === 409 ? `There is already a shelf called ${n}.` : err.message, 'error');
    } finally {
      busy = false;
    }
  }
</script>

<div class="title-row">
  <h1>Shelves</h1>
  {#if !creating}
    <button class="btn primary" onclick={() => (creating = true)}><Plus size={18} /> New shelf</button>
  {/if}
</div>
<p class="lede muted">Shelves are what you send to the reader. A hand-picked shelf holds the books you put on it; a saved search holds whatever matches, and keeps up as the library changes.</p>

{#if creating}
  <form class="panel create" onsubmit={create}>
    <h2>New shelf</h2>
    <label class="field"><span>Name</span><input type="text" bind:value={name} required autocomplete="off" placeholder="To read" /></label>
    <fieldset>
      <legend class="label">Kind</legend>
      <label class="check"><input type="radio" bind:group={kind} value="manual" /><span><strong>Hand-picked</strong><br /><span class="muted small">Add books one by one from their pages.</span></span></label>
      <label class="check"><input type="radio" bind:group={kind} value="query" /><span><strong>Saved search</strong><br /><span class="muted small">Every book matching a search, like <code>tag:queue</code> or <code>author:"le guin"</code>.</span></span></label>
    </fieldset>
    {#if kind === 'query'}
      <label class="field"><span>Search</span><input type="text" bind:value={query} placeholder='series:Earthsea or tag:queue' autocomplete="off" spellcheck="false" /></label>
    {/if}
    <div class="row">
      <button class="btn primary" disabled={busy}>Make shelf</button>
      <button type="button" class="btn ghost" onclick={() => (creating = false)}>Cancel</button>
    </div>
  </form>
{/if}

{#if error}<div class="notice error">{error}</div>{/if}

{#if shelves === null}
  <Loading />
{:else if !shelves.length && !error}
  <Empty title="No shelves yet">
    <p>Make one to choose what goes to the reader. Without shelves, a sync sends the whole library.</p>
  </Empty>
{:else}
  <ul class="shelves">
    {#each shelves as s (s.name)}
      <li>
        <a href={shelfPath(s.name)}>
          <span class="icon" aria-hidden="true">{#if s.kind === 'query'}<Search size={18} />{:else}<Hand size={18} />{/if}</span>
          <span class="main">
            <span class="name">{s.name}</span>
            <span class="muted small">{#if s.kind === 'query'}Saved search · <code>{s.query}</code>{:else}Hand-picked{/if}</span>
          </span>
          <span class="count">{s.books} <span class="muted small">{s.books === 1 ? 'book' : 'books'}</span></span>
        </a>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .title-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
  }

  .lede {
    margin: 0.75rem 0 2.25rem;
    max-width: 40rem;
    font-size: 1.0625rem;
  }

  .create {
    display: grid;
    gap: 1rem;
    max-width: 34rem;
    margin-bottom: 2rem;
  }

  fieldset {
    border: 0;
    padding: 0;
    margin: 0;
    display: grid;
    gap: 0.6rem;
  }

  legend {
    margin-bottom: 0.4rem;
  }

  .shelves {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr));
    gap: 0.75rem;
  }

  .shelves a {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: 0.9rem;
    align-items: center;
    padding: 1.1rem 1.25rem;
    background: var(--tile);
    border-radius: var(--radius-tile);
    color: inherit;
    text-decoration: none;
    transition: background 0.15s var(--ease-out);
  }

  .shelves a:hover {
    background: var(--tile-2);
  }

  .icon {
    color: var(--ink);
    display: grid;
    place-items: center;
    width: 2.2rem;
    height: 2.2rem;
    border-radius: 50%;
    background: var(--ground);
  }

  .main {
    display: grid;
    min-width: 0;
  }

  .main > span:last-child {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .name {
    font-weight: 600;
    font-size: var(--text-body);
    letter-spacing: -0.01em;
  }

  .count {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
    text-align: right;
  }
</style>
