<script>
  import { X } from '@lucide/svelte';
  import { api } from '../lib/api.js';
  import { session, toast } from '../lib/session.svelte.js';
  import { route, navigate } from '../lib/router.svelte.js';
  import { authors, percent, relativeTime } from '../lib/format.js';
  import { bookPath } from '../lib/routes.js';
  import Cover from '../components/Cover.svelte';
  import ProgressBar from '../components/ProgressBar.svelte';
  import Confirm from '../components/Confirm.svelte';
  import Loading from '../components/Loading.svelte';
  import Empty from '../components/Empty.svelte';

  const user = $derived(new URLSearchParams(route.search).get('user') || '');
  let list = $state(null);
  let error = $state('');
  let other = $state('');
  let clearing = $state(null);
  let confirmOpen = $state(false);

  async function load() {
    try {
      list = await api.progress(user);
      error = '';
    } catch (err) {
      error = err.message;
      list = { user, books: [] };
    }
  }

  $effect(() => {
    void user;
    load();
  });

  const current = $derived((list?.books || []).filter((e) => e.percentage < 99.5));
  const finished = $derived((list?.books || []).filter((e) => e.percentage >= 99.5));

  async function clear() {
    const e = clearing;
    try {
      await api.clearProgress(e.book.id, user);
      list.books = list.books.filter((x) => x.book.id !== e.book.id);
      toast('Reading position cleared.');
    } catch (err) {
      toast(err.message, 'error');
      throw err;
    }
  }

  function showUser(e) {
    e.preventDefault();
    navigate(`/reading${other.trim() ? `?user=${encodeURIComponent(other.trim())}` : ''}`);
  }
</script>

<h1>Reading</h1>
<p class="lede muted">
  Where you are in each book, as the reader last told shelf{#if list?.user}{' for '}<strong>{list.user}</strong>{/if}.
  Positions arrive when the reader syncs progress to this server.
</p>

<form class="who row" onsubmit={showUser}>
  <label class="field">
    <span>Reader account</span>
    <input type="text" bind:value={other} placeholder={list?.user || session.me?.progress_user || 'kosync user'} autocomplete="off" spellcheck="false" />
  </label>
  <button class="btn">Show</button>
</form>

{#if error}
  <div class="notice error">{error}</div>
{:else if list === null}
  <Loading />
{:else if !list.books.length}
  <Empty title="Nothing opened yet">
    <p>Turn on progress sync on the reader, pointed at this server, with a shelf account (<code>shelf user add</code>). Each book you open then shows up here.</p>
  </Empty>
{:else}
  {#each [['Reading now', current], ['Finished', finished]] as [heading, entries] (heading)}
    {#if entries.length}
      <section>
        <h2>{heading}</h2>
        <ul>
          {#each entries as e (e.book.id)}
            <li>
              <a class="item" href={bookPath(e.book.id)}>
                <span class="thumb"><Cover book={e.book} /></span>
                <span class="main">
                  <span class="title">{e.book.title}</span>
                  <span class="muted small">{authors(e.book.authors)}</span>
                  <span class="bar"><ProgressBar value={e.percentage} /><strong>{percent(e.percentage)}</strong></span>
                  <span class="muted small">{#if e.device}{e.device}{' · '}{/if}{relativeTime(e.updated_unix)}</span>
                </span>
              </a>
              <button class="btn ghost icon" aria-label="Clear the position for {e.book.title}" onclick={() => { clearing = e; confirmOpen = true; }}><X size={18} /></button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  {/each}
{/if}

<Confirm
  bind:open={confirmOpen}
  title="Clear this position?"
  body="Shelf forgets where you were in {clearing?.book.title ?? 'this book'}. The reader keeps its own position and sends it again the next time the book is open with sync on, so this is for clearing out stale entries."
  confirmLabel="Clear position"
  danger
  onconfirm={clear}
/>

<style>
  .lede {
    margin: 0.75rem 0 1.75rem;
    max-width: 42rem;
    font-size: 1.0625rem;
  }

  .who {
    align-items: flex-end;
    margin-bottom: 1.75rem;
    max-width: 26rem;
    flex-wrap: nowrap;
  }

  .who .field {
    flex: 1;
  }

  section {
    margin-bottom: 2.75rem;
  }

  section h2 {
    margin-bottom: 1rem;
  }

  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(22rem, 1fr));
    gap: 0.8rem;
  }

  @media (max-width: 480px) {
    ul {
      grid-template-columns: 1fr;
    }
  }

  li {
    display: flex;
    align-items: flex-start;
    background: var(--tile);
    border-radius: var(--radius-tile);
    padding: 1rem 0.5rem 1rem 1rem;
  }

  .item {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: 3.6rem 1fr;
    gap: 0.9rem;
    color: inherit;
    text-decoration: none;
  }

  /* Too small to read: the coloured binding is enough to tell books apart. */
  .thumb :global(.plate) {
    display: none;
  }

  .main {
    display: grid;
    gap: 0.15rem;
    min-width: 0;
    align-content: start;
  }

  .title {
    font-weight: 600;
    letter-spacing: -0.01em;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .bar {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.6rem;
    align-items: center;
    margin: 0.35rem 0 0.1rem;
  }

  .bar strong {
    color: var(--accent);
    font-size: 0.9rem;
    font-variant-numeric: tabular-nums;
  }
</style>
