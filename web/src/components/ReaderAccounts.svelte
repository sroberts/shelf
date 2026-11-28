<script>
  // Reader accounts: what the e-reader signs in with for the catalog and for
  // progress sync. A new or reset password is shown once, with everything the
  // reader's setup screens ask for, and never again: shelf keeps only a hash.
  import { tick } from 'svelte';
  import { Plus, Copy, Check, KeyRound, X } from '@lucide/svelte';
  import { api } from '../lib/api.js';
  import { toast } from '../lib/session.svelte.js';
  import { relativeTime } from '../lib/format.js';
  import Confirm from './Confirm.svelte';

  let accounts = $state(null);
  let error = $state('');
  let creating = $state(false);
  let name = $state('');
  let nameError = $state('');
  let busy = $state(false);
  let nameInput = $state();
  /** The credentials just made: { name, password, reset } until dismissed. */
  let fresh = $state(null);
  let copied = $state('');
  let deleting = $state(null);
  let confirmOpen = $state(false);

  // The address this page was opened at is the one the reader needs too: under
  // Outpost, Funnel answers the reader's public paths at the same host.
  const origin = location.origin;
  const opds = `${origin}/opds`;

  async function load() {
    try {
      accounts = await api.accounts();
      error = '';
    } catch (err) {
      error = err.message;
      accounts = [];
    }
  }

  $effect(() => {
    load();
  });

  async function open() {
    creating = true;
    fresh = null;
    await tick();
    nameInput?.focus();
  }

  function close() {
    creating = false;
    name = '';
    nameError = '';
  }

  async function create(e) {
    e.preventDefault();
    nameError = '';
    if (!name.trim()) {
      nameError = 'Give the account a name, such as x4.';
      nameInput?.focus();
      return;
    }
    busy = true;
    try {
      const acct = await api.createAccount(name.trim());
      fresh = { name: acct.name, password: acct.password, reset: false };
      close();
      load();
    } catch (err) {
      nameError = err.message.charAt(0).toUpperCase() + err.message.slice(1);
      nameInput?.focus();
    } finally {
      busy = false;
    }
  }

  async function reset(acct) {
    try {
      const res = await api.resetPassword(acct.name);
      fresh = { name: res.name, password: res.password, reset: true };
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function remove() {
    try {
      await api.deleteAccount(deleting.name);
      toast(`Deleted ${deleting.name}.`);
      if (fresh?.name === deleting.name) fresh = null;
      load();
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  async function copy(label, text) {
    try {
      await navigator.clipboard.writeText(text);
      copied = label;
      setTimeout(() => copied === label && (copied = ''), 1600);
    } catch {
      toast('Copying isn’t allowed here; select the text instead.', 'error');
    }
  }
</script>

<section class="accounts" aria-labelledby="accounts-title">
  <div class="head">
    <div>
      <h2 id="accounts-title">Reader accounts</h2>
      <p class="muted small">What the reader signs in with, for the catalog and for progress sync. It can’t sign in to Outpost, so it uses one of these.</p>
    </div>
    {#if accounts?.length && !creating}
      <button class="btn" onclick={open}><Plus size={17} /> New account</button>
    {/if}
  </div>

  {#if error}<p class="notice error">{error}</p>{/if}

  {#if fresh}
    <div class="fresh" role="status">
      <div class="fresh-head">
        <h3>{fresh.reset ? `New password for ${fresh.name}` : `${fresh.name} is ready`}</h3>
        <button class="btn ghost icon small" aria-label="Done, hide the password" title="Done" onclick={() => (fresh = null)}><X size={16} /></button>
      </div>
      <p class="small">Enter these on the reader. The password is shown only now: shelf keeps just a fingerprint of it.</p>
      <dl>
        {#snippet row(label, value, mono = true)}
          <dt>{label}</dt>
          <dd>
            <code class:plain={!mono}>{value}</code>
            <button class="btn ghost icon small" aria-label="Copy {label.toLowerCase()}" title="Copy" onclick={() => copy(label, value)}>
              {#if copied === label}<Check size={15} />{:else}<Copy size={15} />{/if}
            </button>
          </dd>
        {/snippet}
        {@render row('OPDS server', opds)}
        {@render row('Sync server', origin)}
        {@render row('Username', fresh.name)}
        {@render row('Password', fresh.password)}
      </dl>
      <p class="muted small">
        On the reader: <strong>Settings → OPDS servers</strong> for the catalog, and <strong>KOReader sync</strong> for reading
        positions, using <strong>Log in</strong>, not Register. Away from home, these addresses only reach shelf if its reader paths are
        open to the internet (Outpost’s Internet access, through Tailscale Funnel).
      </p>
    </div>
  {/if}

  {#if creating || (accounts && !accounts.length)}
    <form class="create" onsubmit={create} novalidate>
      <div class="field">
        <label for="account-name">Account name</label>
        <input id="account-name" type="text" bind:this={nameInput} bind:value={name} autocomplete="off" autocapitalize="off" spellcheck="false"
          aria-invalid={nameError ? 'true' : undefined} aria-describedby={nameError ? 'account-name-error' : 'account-name-help'} />
        {#if nameError}<p class="field-error" id="account-name-error">{nameError}</p>
        {:else}<small id="account-name-help">One per reader is simplest, such as x4. shelf makes up the password.</small>{/if}
      </div>
      <div class="row">
        <button class="btn primary" disabled={busy}><KeyRound size={16} /> {busy ? 'Creating…' : 'Create account'}</button>
        {#if accounts?.length}<button type="button" class="btn ghost" onclick={close}>Cancel</button>{/if}
      </div>
    </form>
  {/if}

  {#if accounts?.length}
    <ul class="list">
      {#each accounts as acct (acct.name)}
        <li>
          <span class="who">
            <span class="name">{acct.name}</span>
            <span class="muted small">{acct.books} {acct.books === 1 ? 'book' : 'books'} with a reading position{#if acct.created_unix}{' · '}made {relativeTime(acct.created_unix)}{/if}</span>
          </span>
          <span class="acts">
            <button class="btn small" onclick={() => reset(acct)}>New password</button>
            <button class="btn small ghost danger-text" onclick={() => { deleting = acct; confirmOpen = true; }}>Delete</button>
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

{#if deleting}
  <Confirm
    bind:open={confirmOpen}
    title="Delete {deleting.name}?"
    body={deleting.books
      ? `Its reading positions in ${deleting.books} ${deleting.books === 1 ? 'book' : 'books'} are deleted with it, and a reader signed in as ${deleting.name} stops syncing. The reader keeps its own positions.`
      : `A reader signed in as ${deleting.name} stops syncing and loses the catalog.`}
    confirmLabel="Delete account"
    danger
    onconfirm={remove}
  />
{/if}

<style>
  .accounts {
    margin-top: 3rem;
    display: grid;
    gap: 1.25rem;
  }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
  }

  .head p {
    margin-top: 0.35rem;
    max-width: 40rem;
  }

  /* The one moment the password exists in the clear: given the page's
     strongest surface, so it is not missed. */
  .fresh {
    display: grid;
    gap: 0.75rem;
    padding: 1.5rem;
    border-radius: var(--radius-tile);
    background: var(--tile);
    animation: rise 0.3s var(--ease-out);
  }

  .fresh-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  dl {
    display: grid;
    grid-template-columns: max-content minmax(0, 1fr);
    gap: 0.4rem 1.5rem;
    margin: 0.25rem 0;
    align-items: center;
  }

  dt {
    color: var(--muted);
    font-size: var(--text-meta);
  }

  dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 0.35rem;
    min-width: 0;
  }

  dd code {
    overflow-wrap: anywhere;
    font-size: 0.9375rem;
    padding: 0.3rem 0.55rem;
    background: var(--ground);
  }

  @media (max-width: 560px) {
    dl {
      grid-template-columns: 1fr;
      gap: 0.15rem;
    }

    dd {
      margin-bottom: 0.6rem;
    }
  }

  .create {
    display: grid;
    gap: 1rem;
    max-width: 28rem;
  }

  .create label {
    font-size: var(--text-meta);
    font-weight: 600;
  }

  .field-error {
    margin: 0;
    font-size: var(--text-meta);
    font-weight: 550;
    color: var(--danger);
  }

  .create input[aria-invalid='true'] {
    border-color: var(--danger);
  }

  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.5rem;
  }

  .list li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    flex-wrap: wrap;
    padding: 0.9rem 1.1rem;
    border-radius: var(--radius);
    background: var(--tile);
  }

  .who {
    display: grid;
    min-width: 0;
  }

  .name {
    font-weight: 600;
  }

  .acts {
    display: flex;
    gap: 0.4rem;
  }

  .danger-text {
    color: var(--danger);
  }

  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
  }
</style>
