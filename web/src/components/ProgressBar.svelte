<script>
  /** @type {{value: number, label?: string}} */
  let { value, label = 'Read' } = $props();
  const v = $derived(Math.min(100, Math.max(0, value || 0)));
</script>

<div class="track" role="progressbar" aria-label={label} aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(v)}>
  <div class="fill" style:width="{v}%"></div>
</div>

<style>
  .track {
    height: 3px;
    border-radius: 2px;
    background: var(--hairline);
    overflow: hidden;
  }

  .fill {
    height: 100%;
    background: var(--accent);
    border-radius: 2px;
  }
</style>
