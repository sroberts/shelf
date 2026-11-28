<script>
  /** @type {{label?: string}} */
  let { label = 'Loading' } = $props();
</script>

<div class="loading" role="status"><span class="dot"></span><span class="muted small">{label}…</span></div>

<style>
  .loading {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    justify-content: center;
    padding: 3rem 1rem;
  }

  .dot {
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 50%;
    background: var(--accent);
    animation: pulse 1s ease-in-out infinite alternate;
  }

  @keyframes pulse {
    from {
      opacity: 0.25;
    }
    to {
      opacity: 1;
    }
  }
</style>
