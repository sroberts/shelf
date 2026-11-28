<script>
  import { tick } from 'svelte';
  import { Plus, Wifi, HardDrive, TriangleAlert, Eye, Send, X, ChevronDown } from '@lucide/svelte';
  import { api } from '../lib/api.js';
  import { feature, toast, loadSession } from '../lib/session.svelte.js';
  import { route } from '../lib/router.svelte.js';
  import { bytes, relativeTime, middle } from '../lib/format.js';
  import {
    isActive,
    planSummary,
    jobFraction,
    stateLabel,
    scopeLabel,
    transportLabel,
    isCard,
    normalizeHost,
    planItems,
    deletions,
  } from '../lib/jobs.js';
  import Loading from '../components/Loading.svelte';
  import Confirm from '../components/Confirm.svelte';
  import ReaderAccounts from '../components/ReaderAccounts.svelte';

  const wanted = new URLSearchParams(route.search).get('shelf') || '';
  const blankForm = () => ({ name: '', host: '', root: '' });

  let devices = $state(null);
  let shelves = $state([]);
  let jobs = $state([]);
  let error = $state('');
  /** Per-device choices: { shelf, prune } */
  let choice = $state({});
  /** The job shown on each device's card, by device name. */
  let current = $state({});
  let adding = $state(false);
  let form = $state(blankForm());
  /** Inline field errors for the add form. @type {{ name?: string, host?: string }} */
  let fieldError = $state({});
  let busy = $state(false);
  let nameInput = $state();
  let hostInput = $state();
  let addSection = $state();
  /** The prune confirmation: { dev, items } while open. */
  let confirming = $state(null);
  let confirmOpen = $state(false);

  async function load() {
    try {
      const [d, s, j] = await Promise.all([api.devices(), api.shelves().catch(() => []), api.jobs().catch(() => [])]);
      devices = d;
      shelves = s;
      jobs = j;
      for (const dev of d) {
        choice[dev.name] ??= { shelf: wanted, prune: false };
        // Show the newest job for each device, so a sync started elsewhere
        // (another tab, an agent over MCP) is seen here too.
        const latest = j.find((x) => x.device === dev.name);
        if (latest && !current[dev.name]) {
          current[dev.name] = latest;
          // A sync under way shows what it is sending, not the default choice.
          if (isActive(latest)) choice[dev.name] = { shelf: latest.shelf, prune: latest.prune };
        }
      }
      error = '';
    } catch (err) {
      error = err.message;
      devices = [];
    }
  }

  async function refreshJobs() {
    try {
      jobs = await api.jobs();
    } catch {
      // the list stays as it was; the next load corrects it
    }
  }

  $effect(() => {
    load();
  });

  // Poll every job still in progress, once a second.
  $effect(() => {
    const active = Object.values(current).filter(isActive);
    if (!active.length) return;
    const t = setTimeout(async () => {
      for (const j of active) {
        try {
          const next = await api.job(j.id);
          current[next.device] = next;
          if (!isActive(next)) finished(next);
        } catch (err) {
          if (err.status === 404) delete current[j.device];
        }
      }
    }, 1000);
    return () => clearTimeout(t);
  });

  function finished(job) {
    if (job.state === 'done' && !job.dry_run) {
      toast(`Synced ${job.shelf || 'the library'} to ${job.device}.`);
      load(); // last-synced time and book count changed
    } else {
      // A failed preview is shown on its card already; a failed sync may
      // finish while someone is looking elsewhere, so it also gets a toast.
      if (job.state === 'failed' && !job.dry_run) toast(`Sync to ${job.device} failed. The details are on its card.`, 'error');
      refreshJobs();
    }
  }

  async function start(dev, dryRun) {
    const c = choice[dev.name];
    try {
      current[dev.name] = await api.sync(dev.name, { shelf: c.shelf, prune: c.prune, dry_run: dryRun });
      refreshJobs();
    } catch (err) {
      if (err.status === 409 && err.body?.job) {
        toast('A sync to this reader is already under way; showing it.', 'info');
        try {
          current[dev.name] = await api.job(err.body.job);
        } catch {
          // shown on the next refresh
        }
      } else toast(err.message, 'error');
    }
  }

  /** Runs the previewed sync, after a confirmation naming every book it deletes. */
  function syncPreviewed(dev) {
    const items = deletions(current[dev.name]?.plan);
    if (choice[dev.name].prune && items.length) {
      confirming = { dev, items };
      confirmOpen = true;
      return;
    }
    start(dev, false);
  }

  async function cancel(job) {
    try {
      current[job.device] = await api.cancelJob(job.id);
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function openForm() {
    adding = true;
    await tick();
    addSection?.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    nameInput?.focus({ preventScroll: true });
  }

  function closeForm() {
    adding = false;
    form = blankForm();
    fieldError = /** @type {{ name?: string, host?: string }} */ ({});
  }

  async function addDevice(e) {
    e.preventDefault();
    fieldError = /** @type {{ name?: string, host?: string }} */ ({});
    const name = form.name.trim();
    const { host, error: hostError } = normalizeHost(form.host);
    if (!name) fieldError.name = 'Give the reader a name, such as X4.';
    if (hostError) fieldError.host = hostError;
    if (fieldError.name || fieldError.host) {
      (fieldError.name ? nameInput : hostInput)?.focus();
      return;
    }
    form.host = host; // show what is saved

    busy = true;
    try {
      const body = { name, host };
      if (form.root.trim()) body.root = form.root.trim();
      await api.addDevice(body);
      toast(`Added ${name}.`);
      closeForm();
      await load();
      loadSession(); // features.sync may have just turned on
    } catch (err) {
      // Put the server's answer on the field it is about.
      if (err.status === 409) {
        fieldError.name = `There is already a reader called ${name}.`;
        nameInput?.focus();
      } else if (err.status === 400) {
        // The server's sentences start lower case, to read well after a prefix.
        fieldError.host = err.message.charAt(0).toUpperCase() + err.message.slice(1) + (/[.!?]$/.test(err.message) ? '' : '.');
        hostInput?.focus();
      } else toast(err.message, 'error');
    } finally {
      busy = false;
    }
  }

  /** Whether the job on the card is a finished preview matching what is chosen now. */
  const previewMatches = (dev) => {
    const j = current[dev.name];
    const c = choice[dev.name];
    return Boolean(j && j.dry_run && j.state === 'done' && j.shelf === c.shelf && j.prune === c.prune);
  };

  const hasChanges = (plan) => Boolean(plan && (plan.uploads || plan.deletes || plan.moves));

  /** A short sentence for screen readers, updated per book rather than per byte. */
  function liveStatus(job) {
    if (!job) return '';
    if (job.state === 'running' && job.progress) return `Sending ${job.progress.index} of ${job.progress.total}.`;
    return `${stateLabel(job)}.`;
  }

  const showForm = $derived(adding || (devices !== null && !devices.length));
</script>

<div class="title-row">
  <h1>Readers</h1>
  {#if devices?.length}
    <button class="btn" onclick={openForm} aria-expanded={adding} aria-controls="add-reader"><Plus size={18} /> Add a reader</button>
  {/if}
</div>
<p class="lede muted">Send a shelf to your e-reader. Preview first to see exactly what will change; books already on the reader stay where they are, so their reading positions are kept.</p>

{#if error}<div class="notice error">{error}</div>{/if}

{#if devices === null}
  <Loading />
{:else}
  {#each devices as dev (dev.name)}
    {@const job = current[dev.name]}
    {@const c = choice[dev.name]}
    {@const matched = previewMatches(dev)}
    {@const willDelete = matched && c.prune ? deletions(job.plan).length : 0}
    <section class="panel device" aria-labelledby="dev-{dev.name}">
      <header>
        <span class="icon" aria-hidden="true">{#if isCard(dev)}<HardDrive size={20} />{:else}<Wifi size={20} />{/if}</span>
        <div class="who">
          <h2 id="dev-{dev.name}">{dev.name}{#if dev.default}<span class="badge">Default</span>{/if}</h2>
          <p class="muted small">
            {transportLabel(dev)}{#if isCard(dev) && dev.mount}{' · '}<span class="mono" title={dev.mount}>{middle(dev.mount, 32)}</span>{:else if dev.host}{' · '}<span class="mono">{dev.host}</span>{/if}{#if dev.root && dev.root !== '/'}{' · '}<span class="mono">{dev.root}</span>{/if}
          </p>
        </div>
        <p class="last muted small">
          {#if dev.last_sync_unix}Last synced {relativeTime(dev.last_sync_unix)}{#if dev.books !== undefined}{' · '}{dev.books} {dev.books === 1 ? 'book' : 'books'} on it{/if}{:else}Never synced{/if}
        </p>
      </header>

      {#if feature('sync')}
        <div class="controls">
          <label class="field send"><span>What to send</span>
            <select bind:value={c.shelf} disabled={isActive(job)}>
              <option value="">Whole library</option>
              {#each shelves as s (s.name)}<option value={s.name}>{s.name} ({s.books})</option>{/each}
            </select>
          </label>
          <div class="row buttons">
            <button class="btn" class:primary={!matched} onclick={() => start(dev, true)} disabled={isActive(job)}>
              <Eye size={17} /> {matched ? 'Preview again' : 'Preview'}
            </button>
            {#if !c.prune && !matched}
              <button class="btn" onclick={() => start(dev, false)} disabled={isActive(job)}><Send size={17} /> Sync without preview</button>
            {/if}
          </div>
        </div>

        <details class="more" open={c.prune}>
          <summary><ChevronDown size={16} aria-hidden="true" /> More options</summary>
          <label class="check">
            <input type="checkbox" bind:checked={c.prune} disabled={isActive(job)} />
            <span>Also remove books that aren’t in {c.shelf ? `“${c.shelf}”` : 'the library'}<br /><span class="muted small">Deletes them from the reader, with their reading positions. You’ll see the list and confirm first.</span></span>
          </label>
          {#if c.prune && !matched}
            <p class="notice warn small"><TriangleAlert size={16} aria-hidden="true" /><span>Preview to see which books would be deleted. Nothing is removed until you confirm.</span></p>
          {/if}
        </details>
      {:else}
        <p class="notice small">Syncing from here isn’t available on this server.</p>
      {/if}

      {#if job}
        <div class="job">
          <p class="visually-hidden" aria-live="polite">{liveStatus(job)}</p>
          <div class="job-head">
            <span class="state {job.state}">{stateLabel(job)}</span>
            <span class="muted small scope">{scopeLabel(job)}{#if job.prune}{' · '}removing extras{/if}{' · '}{relativeTime(job.started_unix)}</span>
            {#if isActive(job)}
              <button class="btn small" onclick={() => cancel(job)}><X size={15} /> Cancel</button>
            {:else}
              <button class="btn small ghost icon-only" onclick={() => delete current[dev.name]} aria-label="Dismiss this result" title="Dismiss"><X size={16} /></button>
            {/if}
          </div>

          {#if job.state === 'planning'}
            <p class="muted small">Reading the reader and preparing books. Converting a large PDF can take a few minutes.</p>
          {/if}

          {#if job.state === 'running' && job.progress}
            <div class="progress">
              <div class="track"><div style:width="{jobFraction(job) * 100}%"></div></div>
              <p class="small"><strong>{job.progress.index} of {job.progress.total}</strong>{' '}<span class="muted">{planItems({ ops: [{ kind: 'upload', path: job.progress.path }] }).items[0].name}</span></p>
              <p class="muted small">{bytes(job.progress.bytes_sent || 0)} sent</p>
            </div>
          {/if}

          {#if job.plan}
            {@const view = planItems(job.plan)}
            <p class="summary">{planSummary(job.plan)}</p>
            {#if job.plan.orphans && !job.prune}
              <p class="muted small">{job.plan.orphans} file{job.plan.orphans === 1 ? '' : 's'} on the reader {job.plan.orphans === 1 ? 'isn’t' : 'aren’t'} in shelf’s records; left alone.</p>
            {/if}
            {#each job.plan.warnings || [] as w (w)}<p class="notice warn small"><TriangleAlert size={16} aria-hidden="true" /><span>{w}</span></p>{/each}
            {#if view.items.length}
              <details class="plan">
                <summary>Show the {view.items.length === 1 ? 'book' : `${view.items.length} books`}{job.plan.total_bytes ? ` (${bytes(job.plan.total_bytes)})` : ''}</summary>
                <ul class="ops">
                  {#each view.items as op, i (i)}
                    <li>
                      <span class="kind {op.kind}">{op.verb}</span>
                      <span class="name" title={op.kind === 'move' && op.from ? `${op.from} → ${op.path}` : op.path}>{op.name}</span>
                      <span class="size muted">{op.size ? bytes(op.size) : ''}</span>
                    </li>
                  {/each}
                </ul>
                {#if view.folders}<p class="muted small">Plus {view.folders} new folder{view.folders === 1 ? '' : 's'} to hold them.</p>{/if}
                {#if job.plan.truncated}<p class="muted small">Only the first {job.plan.ops.length} changes are listed.</p>{/if}
              </details>
            {/if}
          {/if}

          {#if job.result}
            <p class="small">
              {job.result.completed} done{#if job.result.failed}, <strong class="bad">{job.result.failed} failed</strong>{/if}{#if job.result.skipped}, {job.result.skipped} skipped{/if}{' · '}{bytes(job.result.bytes_sent || 0)} sent
            </p>
            {#each job.result.errors || [] as e (e)}<p class="notice error small">{e}</p>{/each}
          {/if}
          {#each job.notes || [] as n (n)}<p class="muted small">{n}</p>{/each}
          {#if job.error}<p class="notice error small">{job.error}</p>{/if}

          {#if matched && hasChanges(job.plan)}
            {#if willDelete}
              <button class="btn danger solid" onclick={() => syncPreviewed(dev)}><TriangleAlert size={17} /> Sync and delete {willDelete}…</button>
            {:else}
              <button class="btn primary" onclick={() => syncPreviewed(dev)}><Send size={17} /> Sync now</button>
            {/if}
          {/if}
        </div>
      {/if}
    </section>
  {/each}

  {#if showForm}
    <section class="panel add" id="add-reader" bind:this={addSection} aria-labelledby="add-title">
      <h2 id="add-title">{devices.length ? 'Add a reader' : 'Add your reader'}</h2>
      <p class="muted small intro">The reader and this server need to be on the same network.</p>
      <form onsubmit={addDevice} novalidate>
        <div class="field">
          <label for="reader-name">Name</label>
          <input id="reader-name" type="text" bind:this={nameInput} bind:value={form.name} autocomplete="off"
            aria-invalid={fieldError.name ? 'true' : undefined} aria-describedby={fieldError.name ? 'reader-name-error' : 'reader-name-help'} />
          {#if fieldError.name}<p class="field-error" id="reader-name-error">{fieldError.name}</p>
          {:else}<small id="reader-name-help">What to call it here, such as X4.</small>{/if}
        </div>
        <div class="field">
          <label for="reader-host">Address</label>
          <input id="reader-host" type="text" bind:this={hostInput} bind:value={form.host} autocomplete="off" spellcheck="false" inputmode="url" autocapitalize="off"
            aria-invalid={fieldError.host ? 'true' : undefined} aria-describedby="reader-host-help{fieldError.host ? ' reader-host-error' : ''}" />
          <small id="reader-host-help">On the reader, open <strong>File transfer</strong>; it shows the address, such as 192.168.1.40.</small>
          {#if fieldError.host}<p class="field-error" id="reader-host-error">{fieldError.host}</p>{/if}
        </div>
        <div class="field">
          <label for="reader-root">Folder on the reader <span class="optional">(optional)</span></label>
          <input id="reader-root" type="text" bind:value={form.root} autocomplete="off" spellcheck="false" autocapitalize="off" aria-describedby="reader-root-help" />
          <small id="reader-root-help">Where books go, such as /Books. Leave empty for the top of the card.</small>
        </div>
        <div class="row actions">
          <button class="btn primary" disabled={busy}>{busy ? 'Adding…' : 'Add reader'}</button>
          {#if devices.length}<button type="button" class="btn ghost" onclick={closeForm}>Cancel</button>{/if}
        </div>
      </form>
    </section>
  {/if}
{/if}

{#if feature('accounts')}<ReaderAccounts />{/if}

{#if jobs.length}
  <section class="history" aria-labelledby="history-title">
    <h2 id="history-title">Recent syncs</h2>
    <ul>
      {#each jobs.slice(0, 12) as j (j.id)}
        <li>
          <span class="state {j.state}">{stateLabel(j)}</span>
          <span class="h-reader">{j.device}</span>
          <span class="h-scope muted">{scopeLabel(j)}</span>
          <span class="h-result muted small">{#if j.result}{j.result.completed} done{#if j.result.failed}, {j.result.failed} failed{/if}{:else if j.plan && j.dry_run}{j.plan.uploads} to send{/if}</span>
          <span class="h-when muted small">{relativeTime(j.started_unix)}</span>
        </li>
      {/each}
    </ul>
  </section>
{/if}

{#if confirming}
  <Confirm
    bind:open={confirmOpen}
    title="Delete {confirming.items.length} {confirming.items.length === 1 ? 'book' : 'books'} from {confirming.dev.name}?"
    confirmLabel="Delete and sync"
    danger
    onconfirm={() => start(confirming.dev, false)}
  >
    {#if choice[confirming.dev.name].shelf}
      <p class="muted">These aren’t on “{choice[confirming.dev.name].shelf}”. They’re deleted from the reader with their reading positions. Any that are in your library stay there.</p>
    {:else}
      <p class="muted">These aren’t in your library, so shelf has no copy of them. They’re deleted from the reader with their reading positions.</p>
    {/if}
    <ul class="doomed">
      {#each confirming.items as item, i (i)}<li title={item.path}>{item.name}</li>{/each}
    </ul>
  </Confirm>
{/if}

<style>
  .title-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
  }

  .lede {
    margin: 0.5rem 0 1.5rem;
    max-width: 44rem;
  }

  .mono {
    font-family: var(--mono);
    font-size: 0.82rem;
  }

  /* The add form: one column, labels over fields, help under the field it
     explains. Spacing inside a field is tight; between fields, generous. */
  .add {
    margin-top: 2rem;
    display: grid;
    gap: 0.35rem;
  }

  .add .intro {
    margin: 0 0 0.9rem;
  }

  .add form {
    display: grid;
    gap: 1.25rem;
    max-width: 28rem;
  }

  .add .field {
    gap: 0.35rem;
    align-content: start;
  }

  .add label {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--ink);
  }

  .optional {
    font-weight: 400;
    color: var(--muted);
  }

  .add small {
    line-height: 1.45;
  }

  .field-error {
    margin: 0;
    font-size: 0.85rem;
    font-weight: 550;
    color: var(--danger);
  }

  .add input[aria-invalid='true'] {
    border-color: var(--danger);
  }

  .add .actions {
    margin-top: 0.25rem;
  }

  .device {
    display: grid;
    gap: 1.1rem;
    margin-bottom: 1.25rem;
  }

  .device header {
    display: grid;
    grid-template-columns: auto 1fr auto;
    gap: 0.9rem;
    align-items: center;
  }

  .who {
    min-width: 0;
  }

  .who h2 {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 0.55rem;
  }

  @media (max-width: 560px) {
    .device header {
      grid-template-columns: auto 1fr;
    }

    .last {
      grid-column: 1 / -1;
    }
  }

  .icon {
    color: var(--ink);
    display: grid;
    place-items: center;
    width: 2.5rem;
    height: 2.5rem;
    border-radius: 50%;
    background: var(--ground);
  }

  .badge {
    font-family: var(--sans);
    font-size: 0.72rem;
    font-weight: 650;
    letter-spacing: 0.02em;
    line-height: 1.5;
    color: var(--ink-2);
    background: var(--tile-2);
    border-radius: 999px;
    padding: 0 0.5rem;
  }

  .controls {
    display: grid;
    grid-template-columns: minmax(12rem, 20rem) auto;
    gap: 1rem;
    align-items: end;
    justify-content: start;
  }

  @media (max-width: 640px) {
    .controls {
      grid-template-columns: 1fr;
    }

    .controls .buttons .btn {
      flex: 1;
    }
  }

  .more {
    display: grid;
    gap: 0.75rem;
  }

  .more summary {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    width: fit-content;
    min-height: 2rem;
    cursor: pointer;
    list-style: none;
    font-size: 0.9rem;
    font-weight: 600;
    color: var(--ink-2);
  }

  .more summary::-webkit-details-marker {
    display: none;
  }

  .more summary :global(svg) {
    transition: transform 0.2s cubic-bezier(0.16, 1, 0.3, 1);
    transform: rotate(-90deg);
  }

  .more[open] summary :global(svg) {
    transform: none;
  }

  .more .check {
    max-width: 40rem;
  }

  .notice.warn {
    display: flex;
    gap: 0.5rem;
    align-items: flex-start;
    margin: 0;
  }

  .notice.warn :global(svg) {
    flex: none;
    margin-top: 0.15rem;
    color: var(--caution);
  }

  .job {
    border-top: 1px solid var(--hairline);
    padding-top: 1rem;
    display: grid;
    gap: 0.6rem;
    justify-items: start;
  }

  .job > * {
    max-width: 100%;
  }

  .job-head {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 0.7rem;
    width: 100%;
  }

  .scope {
    min-width: 0;
  }

  /* On a phone the description takes its own line under the state. */
  @media (max-width: 560px) {
    .job-head {
      grid-template-columns: auto 1fr auto;
      grid-template-areas:
        'state . action'
        'scope scope scope';
      row-gap: 0.35rem;
    }

    .job-head .state {
      grid-area: state;
    }

    .job-head .scope {
      grid-area: scope;
    }

    .job-head .btn {
      grid-area: action;
    }
  }

  .icon-only {
    padding: 0.25rem;
    min-width: 2rem;
  }

  .summary {
    margin: 0;
    font-weight: 550;
  }

  .state {
    justify-self: start;
    font-size: 0.8rem;
    font-weight: 650;
    padding: 0.1rem 0.6rem;
    border-radius: 999px;
    background: var(--tile-2);
    color: var(--ink-2);
    white-space: nowrap;
  }

  .state.planning,
  .state.running {
    background: var(--accent);
    color: var(--accent-ink);
  }

  .state.done {
    background: var(--ground);
    color: var(--ink);
  }

  .state.failed {
    background: var(--danger-wash);
    color: var(--danger);
  }

  .progress {
    width: 100%;
    display: grid;
    gap: 0.35rem;
  }

  .track {
    height: 6px;
    border-radius: 3px;
    background: var(--hairline);
    overflow: hidden;
  }

  /* Gilt, like a page edge: the same colour reading progress uses. */
  .track div {
    height: 100%;
    background: var(--accent);
    transition: width 0.4s cubic-bezier(0.16, 1, 0.3, 1);
  }

  .plan {
    width: 100%;
  }

  .plan summary {
    cursor: pointer;
    font-weight: 600;
    font-size: 0.9rem;
    color: var(--ink-2);
    min-height: 2rem;
    display: flex;
    align-items: center;
  }

  .ops {
    list-style: none;
    margin: 0.5rem 0;
    padding: 0;
    max-height: 18rem;
    overflow-y: auto;
    display: grid;
    gap: 0.3rem;
    font-size: 0.9rem;
  }

  .ops li {
    display: grid;
    grid-template-columns: 4rem minmax(0, 1fr) auto;
    gap: 0.75rem;
    align-items: baseline;
  }

  .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .size {
    font-size: 0.82rem;
    font-variant-numeric: tabular-nums;
  }

  .kind {
    font-size: 0.8rem;
    font-weight: 650;
    color: var(--muted);
  }

  .kind.upload {
    color: var(--ink);
  }

  .kind.delete {
    color: var(--danger);
  }

  .bad {
    color: var(--danger);
  }

  .btn.danger.solid {
    background: var(--danger);
    color: var(--ground);
  }

  .btn.danger.solid:hover {
    background: color-mix(in srgb, var(--danger) 85%, var(--ink));
  }

  .doomed {
    margin: 0;
    padding-left: 1.2rem;
    max-height: 14rem;
    overflow-y: auto;
    display: grid;
    gap: 0.2rem;
  }

  .history {
    margin-top: 2.5rem;
  }

  .history h2 {
    font-size: 1.1rem;
    margin-bottom: 0.6rem;
  }

  .history ul {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--hairline);
  }

  /* Columns, so states, readers and times line up from row to row. */
  .history li {
    display: grid;
    grid-template-columns: 8.5rem minmax(5rem, 1fr) minmax(6rem, 1.4fr) 7rem 7rem;
    gap: 0.4rem 1rem;
    align-items: center;
    padding: 0.6rem 0.2rem;
    border-bottom: 1px solid var(--hairline);
  }

  .h-when {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }

  @media (max-width: 640px) {
    .history li {
      grid-template-columns: auto 1fr auto;
      grid-template-areas:
        'state reader when'
        'scope scope result';
    }

    .history .state {
      grid-area: state;
    }

    .h-reader {
      grid-area: reader;
    }

    .h-when {
      grid-area: when;
    }

    .h-scope {
      grid-area: scope;
    }

    .h-result {
      grid-area: result;
      text-align: right;
    }
  }
</style>
