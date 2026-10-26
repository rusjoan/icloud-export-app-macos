<script>
  import { onMount } from 'svelte'
  import { post, subscribe } from './lib/api.js'
  import Login from './lib/Login.svelte'
  import TwoFactor from './lib/TwoFactor.svelte'
  import Setup from './lib/Setup.svelte'
  import Progress from './lib/Progress.svelte'

  let st = $state(null)
  let connected = $state(false)

  onMount(() => {
    // внешние ссылки — в браузер по умолчанию (окно приложения их не открывает)
    const onClick = (e) => {
      const a = e.target.closest('a[target="_blank"]')
      if (!a) return
      e.preventDefault()
      post('/api/open-url', { url: a.href }).catch(() => window.open(a.href, '_blank'))
    }
    document.addEventListener('click', onClick)
    const unsub = subscribe((s) => {
      st = s
      connected = true
    })
    return () => {
      unsub()
      document.removeEventListener('click', onClick)
    }
  })

  const exporting = $derived(st?.export && st.export.phase !== 'need_login')
  const needRelogin = $derived(st?.export?.phase === 'need_login')

  async function logout() {
    try {
      await post('/api/logout')
    } catch (e) {
      alert(e.message)
    }
  }
</script>

<header class="app-header px-4 py-2 d-flex align-items-center gap-3">
  <i class="bi bi-cloud-arrow-down-fill fs-4 text-primary"></i>
  <div class="fw-semibold">iCloud Export</div>
  <div class="ms-auto d-flex align-items-center gap-3 small text-body-secondary">
    {#if st?.auth === 'ok'}
      <span class="selectable"><i class="bi bi-person-circle me-1"></i>{st.appleId}</span>
      {#if !exporting}
        <button class="btn btn-sm btn-outline-secondary" onclick={logout}>Выйти</button>
      {/if}
    {/if}
    {#if st}<span>v{st.version}</span>{/if}
  </div>
</header>

<main class="container py-4" style="max-width: 960px">
  {#if !connected}
    <div class="text-center text-body-secondary py-5">
      <div class="spinner-border mb-3"></div>
      <div>Подключение…</div>
    </div>
  {:else if st.auth === 'restoring'}
    <div class="text-center text-body-secondary py-5">
      <div class="spinner-border mb-3"></div>
      <div>Восстанавливаю сессию iCloud…</div>
    </div>
  {:else if st.auth === 'none' || needRelogin}
    {#if needRelogin}
      <div class="alert alert-warning">
        <i class="bi bi-pause-circle me-2"></i>{st.export.message}
      </div>
    {/if}
    <Login appleId={st.appleId} />
  {:else if st.auth === '2fa'}
    <TwoFactor phones={st.phones} />
  {:else if st.export}
    <Progress p={st.export} />
  {:else}
    <Setup {st} />
  {/if}
</main>
