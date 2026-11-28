<script>
  /** @type {{title: string, children?: import('svelte').Snippet}} */
  let { title, children } = $props();
</script>

<div class="empty">
  <svg viewBox="0 0 64 40" width="72" aria-hidden="true">
    <rect x="6" y="6" width="9" height="28" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.5" />
    <rect x="17" y="10" width="8" height="24" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.5" />
    <rect x="31" y="9" width="9" height="25" rx="1.5" transform="rotate(14 35 21)" fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="3 3" />
    <line x1="2" y1="35" x2="62" y2="35" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
  </svg>
  <h2>{title}</h2>
  {#if children}<div class="muted">{@render children()}</div>{/if}
</div>

<style>
  .empty {
    display: grid;
    justify-items: center;
    text-align: center;
    gap: 0.6rem;
    padding: 3.5rem 1rem;
    color: var(--muted);
  }

  h2 {
    color: var(--ink);
  }

  .empty > div {
    max-width: 32rem;
  }
</style>
