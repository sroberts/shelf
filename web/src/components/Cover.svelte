<script>
  // A book's cover, or a typeset stand-in when it has none. The stand-in is
  // built from the title and author so a shelf of coverless books still reads
  // as a shelf of different books.
  import { coverUrl } from '../lib/api.js';
  import { coverPalette, authors } from '../lib/format.js';

  /** @type {{book: any, eager?: boolean}} */
  let { book, eager = false } = $props();

  let failed = $state(false);
  const palette = $derived(coverPalette(book.title + (book.authors?.[0] ?? '')));
  // sha256 changes whenever the file does, so a metadata edit busts the cache.
  const src = $derived(`${coverUrl(book.id)}?v=${(book.sha256 || '').slice(0, 12)}`);
</script>

<div class="cover" style:--bg={palette.background} style:--plate-ink={palette.ink} style:--plate-rule={palette.rule} style:--plate-sub={palette.sub}>
  {#if book.has_cover && !failed}
    <img {src} alt="" loading={eager ? 'eager' : 'lazy'} decoding="async" onerror={() => (failed = true)} />
  {:else}
    <div class="plate {palette.layout}" aria-hidden="true">
      <span class="title">{book.title}</span>
      <span class="rule"></span>
      <span class="author">{authors(book.authors)}</span>
    </div>
  {/if}
</div>

<style>
  /* A printed book, not a UI card: near-square corners and a real contact
     shadow, so it sits on its tile like an object on a table. */
  .cover {
    position: relative;
    aspect-ratio: 2 / 3;
    width: 100%;
    border-radius: var(--radius-cover);
    overflow: hidden;
    background: var(--bg);
    box-shadow: var(--lift);
  }

  /* The spine's fold, so a flat image reads as a bound book. */
  .cover::after {
    content: '';
    position: absolute;
    inset: 0 auto 0 0;
    width: 6%;
    background: linear-gradient(90deg, rgb(0 0 0 / 0.14), rgb(255 255 255 / 0.05) 55%, transparent);
    pointer-events: none;
  }

  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  .plate {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    gap: 0.55em;
    padding: 14% 12% 16% 15%;
    color: var(--plate-ink);
    font-family: var(--sans);
    container-type: inline-size;
  }

  /* Three arrangements, chosen from the title like the colour, so two
     stand-ins that happen to share a hue still read as different books. */
  .plate.head {
    justify-content: flex-start;
  }

  .plate.band {
    justify-content: center;
    align-items: center;
    text-align: center;
    padding-left: 14%;
  }

  .title {
    font-size: clamp(0.7rem, 12cqi, 1.6rem);
    font-weight: 700;
    line-height: 1.08;
    letter-spacing: -0.02em;
    display: -webkit-box;
    -webkit-line-clamp: 5;
    line-clamp: 5;
    -webkit-box-orient: vertical;
    overflow: hidden;
    overflow-wrap: anywhere;
  }

  .rule {
    display: block;
    width: 24%;
    height: 2px;
    background: var(--plate-rule);
  }

  .author {
    font-size: clamp(0.55rem, 7.5cqi, 0.95rem);
    font-weight: 500;
    color: var(--plate-sub);
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
