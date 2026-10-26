<script>
  import { post } from './api.js'

  let { appleId = '' } = $props()
  // поле заполняется один раз прошлым Apple ID, дальше его правит пользователь
  // svelte-ignore state_referenced_locally
  let id = $state(appleId)
  let password = $state('')
  let busy = $state(false)
  let error = $state('')

  async function submit(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await post('/api/login', { appleId: id, password })
      password = ''
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }
</script>

<div class="row justify-content-center">
  <div class="col-md-8 col-lg-6">
    <div class="card shadow-sm">
      <div class="card-body p-4">
        <h5 class="card-title mb-1">Вход в iCloud</h5>
        <p class="text-body-secondary small mb-4">Нужен для доступа к медиатеке iCloud Photos.</p>

        <form onsubmit={submit}>
          <div class="mb-3">
            <label class="form-label" for="appleid">Apple ID</label>
            <input id="appleid" class="form-control" type="email" autocomplete="username" bind:value={id} required disabled={busy} />
          </div>
          <div class="mb-3">
            <label class="form-label" for="password">Пароль Apple ID</label>
            <input id="password" class="form-control" type="password" autocomplete="current-password" bind:value={password} required disabled={busy} />
          </div>

          <div class="alert alert-info small py-2">
            <i class="bi bi-info-circle me-1"></i>
            Нужен <b>основной пароль</b> Apple ID. Пароль приложения, который создаётся на
            <a href="https://account.apple.com/account/manage" target="_blank" rel="noreferrer">account.apple.com</a>
            (<a href="https://support.apple.com/ru-ru/102654" target="_blank" rel="noreferrer">подробнее</a>),
            здесь не подойдёт: Apple принимает такие пароли только для почты, календарей и контактов, а для iCloud Photos — нет.
            Пароль не сохраняется: после входа хранится только токен сессии.
          </div>

          {#if error}
            <div class="alert alert-danger small py-2 selectable">{error}</div>
          {/if}

          <button class="btn btn-primary w-100" type="submit" disabled={busy}>
            {#if busy}<span class="spinner-border spinner-border-sm me-2"></span>{/if}
            Войти
          </button>
        </form>
      </div>
    </div>
    <p class="small text-body-secondary mt-3">
      <i class="bi bi-shield-lock me-1"></i>
      Если включена «Расширенная защита данных», включите на iPhone:
      Настройки → [имя] → iCloud → «Доступ к данным iCloud через веб».
    </p>
  </div>
</div>
