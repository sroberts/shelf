<script>
  // A confirmation dialog. Open it by setting `open`; it calls onconfirm and
  // closes itself.
  /** @type {{open: boolean, title: string, body?: string, confirmLabel?: string, danger?: boolean, onconfirm: () => void | Promise<void>, children?: import('svelte').Snippet}} */
  let { open = $bindable(false), title, body = '', confirmLabel = 'Confirm', danger = false, onconfirm, children } = $props();

  let dialog = $state();
  let busy = $state(false);

  $effect(() => {
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  });

  async function confirm() {
    busy = true;
    try {
      await onconfirm();
      open = false;
    } finally {
      busy = false;
    }
  }
</script>

<dialog bind:this={dialog} onclose={() => (open = false)} aria-labelledby="confirm-title">
  <div class="body">
    <h2 id="confirm-title">{title}</h2>
    {#if body}<p class="muted">{body}</p>{/if}
    {@render children?.()}
  </div>
  <div class="actions">
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
    <button class="btn {danger ? 'danger' : 'primary'}" onclick={confirm} disabled={busy}>{confirmLabel}</button>
  </div>
</dialog>

<style>
  .body {
    padding: 1.3rem 1.4rem 0.6rem;
    display: grid;
    gap: 0.6rem;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.6rem;
    padding: 1rem 1.4rem 1.3rem;
  }
</style>
