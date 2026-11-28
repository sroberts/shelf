<script>
  import { ArrowLeft, Trash2, TabletSmartphone, X } from '@lucide/svelte';
  import { api } from '../lib/api.js';
  import { toast } from '../lib/session.svelte.js';
  import { navigate } from '../lib/router.svelte.js';
  import { bytes } from '../lib/format.js';
  import BookGrid from '../components/BookGrid.svelte';
  import Confirm from '../components/Confirm.svelte';
  import Loading from '../components/Loading.svelte';
  import Empty from '../components/Empty.svelte';

  /** @type {{name: string}} */
  let { name } = $props();

  let shelf = $state(null);
  let error = $state('');
  let confirmDelete = $state(false);
  let view = $state(/** @type {'grid' | 'list'} */ (localStorage.getItem('shelf.view') === 'list' ? 'list' : 'grid'));

  async function load() {
    try {
      shelf = await api.shelf(name);
      document.title = `${shelf.name} · shelf`;
    } catch (err) {
      error = err.message;
    }
  }

  $effect(() => {
    load();
  });

  async function remove(book) {
    try {
      await api.removeFromShelf(name, book.id);
      shelf.contents = shelf.contents.filter((b) => b.id !== book.id);
      shelf.books = shelf.contents.length;
      toast(`Took ${book.title} off ${name}.`);
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function del() {
    try {
      await api.deleteShelf(name);
      toast(`Deleted the shelf ${name}. Its books are still in the library.`);
      navigate('/shelves');
    } catch (err) {
      toast(err.message, 'error');
      throw err;
    }
  }

  const size = $derived(shelf ? shelf.contents.reduce((n, b) => n + (b.size || 0), 0) : 0);
</script>

<a class="back muted small" href="/shelves"><ArrowLeft size={16} /> Shelves</a>

{#if error}
  <Empty title="Couldn’t open this shelf"><p>{error}</p></Empty>
{:else if !shelf}
  <Loading />
{:else}
  <header class="head">
    <div>
      <p class="kind muted">{shelf.kind === 'query' ? 'Saved search' : 'Hand-picked shelf'}</p>
      <h1>{shelf.name}</h1>
      <p class="muted small">
        {shelf.books} {shelf.books === 1 ? 'book' : 'books'} · {bytes(size)}
        {#if shelf.kind === 'query'}{' · '}<code>{shelf.query}</code>{/if}
      </p>
    </div>
    <div class="row">
      <a class="btn" href="/devices?shelf={encodeURIComponent(shelf.name)}"><TabletSmartphone size={17} /> Send to reader</a>
      <button class="btn danger" onclick={() => (confirmDelete = true)}><Trash2 size={17} /> Delete shelf</button>
    </div>
  </header>

  {#if shelf.kind === 'query'}
    <p class="notice small">This shelf follows its search. Books join and leave it as the library changes; to change what is on it, change the search with <code>shelf shelf</code> or make a hand-picked shelf.</p>
  {/if}

  {#if !shelf.contents.length}
    <Empty title="Nothing on this shelf">
      {#if shelf.kind === 'manual'}
        <p>Open a book from the <a href="/">library</a> and choose this shelf under <strong>Shelves</strong>.</p>
      {:else}
        <p>No books match <code>{shelf.query}</code> yet.</p>
      {/if}
    </Empty>
  {:else}
    <div class="books">
      {#if shelf.kind === 'manual'}
        <BookGrid books={shelf.contents} {view}>
          {#snippet actions(book)}
            <button class="btn small ghost take-off" onclick={() => remove(book)} aria-label="Take {book.title} off this shelf"><X size={14} /> Take off</button>
          {/snippet}
        </BookGrid>
      {:else}
        <BookGrid books={shelf.contents} {view} />
      {/if}
    </div>
  {/if}

  <Confirm
    bind:open={confirmDelete}
    title="Delete {shelf.name}?"
    body="This deletes the shelf, not its books: every book stays in the library and on the reader. A later sync of another shelf with prune on could remove them from the reader."
    confirmLabel="Delete shelf"
    danger
    onconfirm={del}
  />
{/if}

<style>
  .back {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    text-decoration: none;
    margin-bottom: 1.25rem;
  }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    flex-wrap: wrap;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .head h1 {
    margin: 0.25rem 0;
  }

  .kind {
    font-size: var(--text-meta);
    font-weight: 550;
  }

  .notice {
    margin-bottom: 1.5rem;
    max-width: 44rem;
  }

  .take-off {
    color: var(--muted);
    padding-left: 0;
  }

  .take-off:hover {
    color: var(--danger);
    background: none;
  }

  .books {
    margin-top: 0.5rem;
  }
</style>
