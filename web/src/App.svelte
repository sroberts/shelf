<script>
  import { onMount } from 'svelte';
  import { Library as LibraryIcon, LibraryBig, BookOpenText, TabletSmartphone, Info, Search, X } from '@lucide/svelte';
  import { route, navigate } from './lib/router.svelte.js';
  import { session, loadSession, toasts, dismiss } from './lib/session.svelte.js';
  import { readLibraryParams, writeLibraryParams } from './lib/query.js';
  import Library from './routes/Library.svelte';
  import Book from './routes/Book.svelte';
  import Shelves from './routes/Shelves.svelte';
  import Shelf from './routes/Shelf.svelte';
  import Reading from './routes/Reading.svelte';
  import Devices from './routes/Devices.svelte';
  import About from './routes/About.svelte';
  import NotFound from './routes/NotFound.svelte';

  onMount(loadSession);

  const nav = [
    { href: '/', label: 'Library', icon: LibraryIcon, active: ['library', 'book'] },
    { href: '/shelves', label: 'Shelves', icon: LibraryBig, active: ['shelves', 'shelf'] },
    { href: '/reading', label: 'Reading', icon: BookOpenText, active: ['reading'] },
    { href: '/devices', label: 'Readers', icon: TabletSmartphone, active: ['devices'] },
  ];

  const titles = {
    library: 'Library',
    book: 'Book',
    shelves: 'Shelves',
    shelf: 'Shelf',
    reading: 'Reading',
    devices: 'Readers',
    about: 'About',
    notfound: 'Not found',
  };

  $effect(() => {
    if (route.name !== 'book' && route.name !== 'shelf') document.title = `${titles[route.name]} · shelf`;
  });

  // The bar's search is the library's search: it writes the library's URL,
  // so typing from any page lands on the matching books.
  let query = $state(readLibraryParams(location.search).q);
  let searchBox = $state();
  $effect(() => {
    const q = route.name === 'library' ? readLibraryParams(route.search).q : '';
    if (document.activeElement !== searchBox) query = q;
  });
  let timer;
  function search(now = false) {
    clearTimeout(timer);
    const go = () => {
      const base = route.name === 'library' ? readLibraryParams(route.search) : {};
      navigate('/' + writeLibraryParams({ ...base, q: query.trim() }), { replace: route.name === 'library' });
    };
    if (now) go();
    else timer = setTimeout(go, 250);
  }

  // "/" focuses search from anywhere, as in most tools of this kind.
  function onkey(e) {
    if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = /** @type {HTMLElement} */ (e.target);
    if (t.closest?.('input, textarea, select, [contenteditable]')) return;
    if (!searchBox || !searchBox.offsetParent) return;
    e.preventDefault();
    searchBox.focus();
    searchBox.select();
  }
</script>

<svelte:window onkeydown={onkey} />

<a class="skip" href="#main">Skip to content</a>

<header class="bar">
  <div class="bar-inner">
    <a class="mark" href="/" aria-label="shelf, the library">
      <svg viewBox="0 0 24 24" aria-hidden="true" width="20" height="20">
        <rect x="3" y="4" width="4" height="15" rx="1" fill="currentColor" />
        <rect x="8.5" y="6" width="3.5" height="13" rx="1" fill="currentColor" opacity="0.55" />
        <rect x="14" y="3.5" width="4" height="15.5" rx="1" transform="rotate(14 16 11)" fill="currentColor" opacity="0.8" />
        <rect x="2" y="20" width="20" height="1.6" rx="0.8" fill="currentColor" />
      </svg>
      <span>shelf</span>
    </a>
    <nav class="top-nav" aria-label="Main">
      {#each nav as item (item.href)}
        <a href={item.href} aria-current={item.active.includes(route.name) ? 'page' : undefined}>{item.label}</a>
      {/each}
    </nav>
    <span class="spacer"></span>
    <form class="find" role="search" onsubmit={(e) => { e.preventDefault(); search(true); }}>
      <label>
        <span class="visually-hidden">Search the library</span>
        <Search size={15} aria-hidden="true" />
        <input bind:this={searchBox} type="search" bind:value={query} oninput={() => search()} placeholder="Search" autocomplete="off" spellcheck="false" />
        {#if query}
          <button type="button" class="clear" aria-label="Clear search" onclick={() => { query = ''; search(true); searchBox.focus(); }}><X size={14} /></button>
        {:else}
          <kbd aria-hidden="true">/</kbd>
        {/if}
      </label>
    </form>
    <a class="btn ghost icon" href="/about" aria-label="About this shelf" title="About" aria-current={route.name === 'about' ? 'page' : undefined}>
      <Info size={18} />
    </a>
  </div>
</header>

<main id="main" tabindex="-1">
  {#if session.signedOut}
    <section class="signed-out">
      <h1>Sign in again</h1>
      <p class="muted">Your sign-in has ended. Reload the page to sign in, then carry on where you were.</p>
      <button class="btn primary" onclick={() => location.reload()}>Reload</button>
    </section>
  {:else if route.name === 'library'}
    <Library />
  {:else if route.name === 'book'}
    {#key route.params.id}<Book id={Number(route.params.id)} />{/key}
  {:else if route.name === 'shelves'}
    <Shelves />
  {:else if route.name === 'shelf'}
    {#key route.params.name}<Shelf name={route.params.name} />{/key}
  {:else if route.name === 'reading'}
    <Reading />
  {:else if route.name === 'devices'}
    <Devices />
  {:else if route.name === 'about'}
    <About />
  {:else}
    <NotFound />
  {/if}
</main>

<nav class="tabs" aria-label="Main">
  {#each nav as item (item.href)}
    {@const Icon = item.icon}
    <a href={item.href} aria-current={item.active.includes(route.name) ? 'page' : undefined}>
      <Icon size={22} strokeWidth={1.75} />
      <span>{item.label}</span>
    </a>
  {/each}
</nav>

<div class="toasts" aria-live="polite">
  {#each toasts.items as t (t.id)}
    <div class="toast {t.kind}" role={t.kind === 'error' ? 'alert' : 'status'}>
      <span>{t.message}</span>
      <button class="btn ghost small" onclick={() => dismiss(t.id)} aria-label="Dismiss">Dismiss</button>
    </div>
  {/each}
</div>

<style>
  .skip {
    position: absolute;
    left: -999px;
    top: 0.5rem;
    z-index: 30;
    background: var(--ground);
    padding: 0.5rem 0.8rem;
    border-radius: var(--radius-sm);
  }

  .skip:focus {
    left: 0.5rem;
  }

  .bar {
    position: sticky;
    top: 0;
    z-index: 20;
    background: color-mix(in srgb, var(--ground) 94%, transparent);
    backdrop-filter: saturate(1.8) blur(20px);
    -webkit-backdrop-filter: saturate(1.8) blur(20px);
    box-shadow: 0 1px 0 var(--hairline);
    padding-top: env(safe-area-inset-top);
  }

  @supports not ((backdrop-filter: blur(1px)) or (-webkit-backdrop-filter: blur(1px))) {
    .bar {
      background: var(--ground);
    }
  }

  .bar-inner {
    max-width: var(--page);
    margin: 0 auto;
    height: var(--bar);
    padding: 0 max(1.5rem, env(safe-area-inset-right)) 0 max(1.5rem, env(safe-area-inset-left));
    display: flex;
    align-items: center;
    gap: 1.75rem;
  }

  .mark {
    display: inline-flex;
    align-items: center;
    gap: 0.45rem;
    color: var(--ink);
    text-decoration: none;
    font-weight: 700;
    font-size: 1.0625rem;
    letter-spacing: -0.03em;
  }

  .top-nav {
    display: flex;
    gap: 0.25rem;
  }

  .top-nav a {
    color: var(--muted);
    text-decoration: none;
    padding: 0.3rem 0.75rem;
    border-radius: 999px;
    font-weight: 550;
    font-size: var(--text-meta);
    transition:
      color 0.15s,
      background 0.15s;
  }

  .top-nav a:hover {
    color: var(--ink);
  }

  .top-nav a[aria-current='page'] {
    color: var(--ink);
    background: var(--tile-2);
  }

  .find label {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    width: 15rem;
    height: 2rem;
    padding: 0 0.35rem 0 0.7rem;
    border-radius: 999px;
    background: var(--tile);
    color: var(--muted);
    transition:
      width 0.3s var(--ease-out),
      background 0.15s;
  }

  .find label:focus-within {
    width: 19rem;
    background: var(--ground);
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .find input {
    flex: 1;
    min-width: 0;
    min-height: 0;
    height: 100%;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--ink);
    font-size: var(--text-meta);
  }

  .find input:focus {
    outline: none;
  }

  .find input::-webkit-search-cancel-button {
    display: none;
  }

  .find kbd {
    font-size: 0.6875rem;
    color: var(--muted);
    background: var(--ground);
    min-width: 1.25rem;
    text-align: center;
  }

  .clear {
    display: grid;
    place-items: center;
    width: 1.5rem;
    height: 1.5rem;
    border: 0;
    border-radius: 50%;
    background: var(--tile-2);
    color: var(--ink-2);
    cursor: pointer;
  }

  .spacer {
    flex: 1;
  }

  main {
    max-width: var(--page);
    margin: 0 auto;
    padding: 2.5rem max(1.5rem, env(safe-area-inset-right)) 5rem max(1.5rem, env(safe-area-inset-left));
    outline: none;
  }

  .signed-out {
    max-width: 30rem;
    margin: 4rem auto;
    display: grid;
    gap: 0.8rem;
    justify-items: start;
  }

  .tabs {
    display: none;
  }

  @media (max-width: 720px) {
    .top-nav,
    .find {
      display: none;
    }

    .bar-inner,
    main {
      padding-left: max(1rem, env(safe-area-inset-left));
      padding-right: max(1rem, env(safe-area-inset-right));
    }

    main {
      padding-top: 1.5rem;
      padding-bottom: calc(var(--tabs) + 2.5rem + env(safe-area-inset-bottom));
    }

    .tabs {
      position: fixed;
      left: 0;
      right: 0;
      bottom: 0;
      z-index: 20;
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      /* Opaque: covers scroll beneath the labels, and a translucent bar let
         their type show through the words "Library" and "Reading". */
      background: var(--ground);
      box-shadow: 0 -1px 0 var(--hairline);
      padding-bottom: env(safe-area-inset-bottom);
    }

    .tabs a {
      height: var(--tabs);
      display: grid;
      place-items: center;
      align-content: center;
      gap: 0.15rem;
      color: var(--muted);
      text-decoration: none;
      font-size: 0.6875rem;
      font-weight: 550;
    }

    .tabs a[aria-current='page'] {
      color: var(--ink);
    }

  }

  .toasts {
    position: fixed;
    z-index: 40;
    left: 50%;
    transform: translateX(-50%);
    bottom: calc(1.25rem + env(safe-area-inset-bottom));
    display: grid;
    gap: 0.5rem;
    width: min(28rem, calc(100vw - 2rem));
  }

  @media (max-width: 720px) {
    .toasts {
      bottom: calc(var(--tabs) + 0.75rem + env(safe-area-inset-bottom));
    }
  }

  .toast {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    justify-content: space-between;
    background: var(--ink);
    color: var(--ground);
    border-radius: var(--radius);
    padding: 0.6rem 0.6rem 0.6rem 1rem;
    box-shadow: var(--float);
    font-size: var(--text-meta);
    animation: toast-in 0.3s var(--ease-out);
  }

  @keyframes toast-in {
    from {
      opacity: 0;
      transform: translateY(10px);
    }
  }

  .toast .btn {
    color: inherit;
  }

  .toast .btn:hover {
    background: color-mix(in srgb, var(--ground) 16%, transparent);
  }

  .toast.error {
    background: var(--danger);
    color: var(--ground);
  }
</style>
