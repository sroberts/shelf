<script>
  import Cover from './Cover.svelte';
  import ProgressBar from './ProgressBar.svelte';
  import { authors, percent, seriesLabel, bytes, year } from '../lib/format.js';
  import { bookPath } from '../lib/routes.js';

  /** @type {{books: any[], view?: 'grid' | 'list', actions?: import('svelte').Snippet<[any]>}} */
  let { books, view = 'grid', actions } = $props();
</script>

{#if view === 'grid'}
  <ul class="grid">
    {#each books as book (book.id)}
      <li>
        <a class="card" href={bookPath(book.id)}>
          <!-- The signature: each book stands on its own plinth, lifted off it. -->
          <span class="tile">
            <span class="book"><Cover {book} /></span>
            {#if book.percent_read !== undefined}
              <!-- How far in, on the plinth's edge under the book, like a bookmark. -->
              <span class="read"><ProgressBar value={book.percent_read} /></span>
            {/if}
          </span>
          <span class="title">{book.title}</span>
          <span class="by-row">
            <span class="by">{authors(book.authors)}</span>
            {#if book.percent_read !== undefined}<span class="pct-label">{percent(book.percent_read)}</span>{/if}
          </span>
        </a>
        {#if actions}<div class="card-actions">{@render actions(book)}</div>{/if}
      </li>
    {/each}
  </ul>
{:else}
  <ul class="list">
    {#each books as book (book.id)}
      <li>
        <a class="line" href={bookPath(book.id)}>
          <span class="thumb"><Cover {book} /></span>
          <span class="main">
            <span class="title">{book.title}</span>
            <span class="by">{authors(book.authors)}{#if book.series}{' · '}{seriesLabel(book.series, book.series_index)}{/if}</span>
          </span>
          <span class="meta">
            <span class="pct">{#if book.percent_read !== undefined}{percent(book.percent_read)}{/if}</span>
            <span class="fmt">{book.format.toUpperCase()}</span>
            <span class="hide-sm">{year(book.pubdate)}</span>
            <span class="hide-sm size">{bytes(book.size)}</span>
          </span>
        </a>
        {#if actions}<div class="line-actions">{@render actions(book)}</div>{/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(12.5rem, 1fr));
    gap: 2.5rem 1.5rem;
  }

  @media (max-width: 560px) {
    .grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 1.75rem 0.75rem;
    }
  }

  .card {
    display: grid;
    gap: 0.15rem;
    color: inherit;
    text-decoration: none;
  }

  .tile {
    position: relative;
    display: grid;
    place-items: end center;
    aspect-ratio: 5 / 6;
    padding: 13% 21% 11%;
    margin-bottom: 0.75rem;
    background: var(--tile);
    border-radius: var(--radius-tile);
    transition: background 0.2s var(--ease-out);
  }

  .book {
    display: block;
    width: 100%;
    transition: transform 0.35s var(--ease-out);
  }

  .book :global(.cover) {
    transition: box-shadow 0.35s var(--ease-out);
  }

  .card:hover .tile {
    background: color-mix(in srgb, var(--tile) 70%, var(--tile-2));
  }

  .card:hover .book {
    transform: translateY(-4px);
  }

  .card:hover .book :global(.cover) {
    box-shadow: var(--lift-hover);
  }

  .card:focus-visible {
    outline: none;
  }

  .card:focus-visible .tile {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  .card .title {
    font-weight: 600;
    font-size: 0.9375rem;
    line-height: 1.3;
    letter-spacing: -0.01em;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .by {
    color: var(--muted);
    font-size: var(--text-meta);
    line-height: 1.35;
  }

  .card .by {
    display: -webkit-box;
    -webkit-line-clamp: 1;
    line-clamp: 1;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }

  .read {
    position: absolute;
    left: 21%;
    right: 21%;
    bottom: 5.5%;
  }

  .by-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.75rem;
    min-width: 0;
  }

  .pct-label {
    flex: none;
    font-size: var(--text-small);
    font-weight: 600;
    color: var(--ink);
    font-variant-numeric: tabular-nums;
  }

  .card-actions {
    margin-top: 0.5rem;
  }

  /* The list is the dense view: one aligned row per book. */
  .list li {
    display: flex;
    align-items: center;
    border-bottom: 1px solid var(--hairline);
  }

  .line {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: 2.25rem minmax(0, 1fr) auto;
    align-items: center;
    gap: 1rem;
    padding: 0.55rem 0.75rem;
    color: inherit;
    text-decoration: none;
    border-radius: var(--radius-sm);
  }

  .line:hover {
    background: var(--tile);
  }

  .thumb {
    width: 2.25rem;
  }

  .thumb :global(.plate) {
    display: none;
  }

  .main {
    min-width: 0;
    display: grid;
  }

  .line .title {
    font-weight: 550;
    font-size: 0.9375rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .line .by {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .meta {
    display: grid;
    grid-template-columns: 3rem 2.75rem 2.75rem 4.5rem;
    gap: 1rem;
    justify-items: end;
    color: var(--muted);
    font-size: var(--text-meta);
    font-variant-numeric: tabular-nums;
  }

  .fmt {
    font-size: var(--text-small);
    font-weight: 550;
  }

  .pct {
    color: var(--ink);
    font-weight: 600;
  }

  .line-actions {
    padding-left: 0.5rem;
  }

  @media (max-width: 600px) {
    .meta {
      grid-template-columns: 3rem 2.75rem;
    }

    .hide-sm {
      display: none;
    }
  }
</style>
