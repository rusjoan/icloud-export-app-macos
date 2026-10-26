<script>
  import { post } from './api.js'

  let { phones = [] } = $props()
  let code = $state('')
  let phoneId = $state(0) // 0 — код с доверенного устройства
  let busy = $state(false)
  let error = $state('')
  let info = $state('')

  async function verify(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      await post('/api/2fa', { code, phoneId })
    } catch (err) {
      error = err.message
      code = ''
    } finally {
      busy = false
    }
  }

  async function sms(id) {
    busy = true
    error = ''
    try {
      await post('/api/2fa/sms', { phoneId: id })
      phoneId = id
      info = 'Код отправлен по SMS'
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
      <div class="card-body p-4 text-center">
        <i class="bi bi-phone fs-1 text-primary"></i>
        <h5 class="mt-2">Код подтверждения</h5>
        <p class="text-body-secondary small">
          {phoneId === 0 ? 'Введите код, который появился на вашем iPhone, iPad или Mac.' : info}
        </p>

        <form onsubmit={verify} class="d-flex flex-column align-items-center gap-3">
          <!-- svelte-ignore a11y_autofocus -->
          <input class="form-control code-input" inputmode="numeric" maxlength="6" autocomplete="one-time-code"
            bind:value={code} autofocus disabled={busy} />
          {#if error}<div class="alert alert-danger small py-2 w-100 selectable">{error}</div>{/if}
          <button class="btn btn-primary px-5" type="submit" disabled={busy || code.trim().length < 6}>
            {#if busy}<span class="spinner-border spinner-border-sm me-2"></span>{/if}
            Подтвердить
          </button>
        </form>

        {#if phones.length}
          <hr />
          <div class="small text-body-secondary mb-2">Нет устройства под рукой? Получите код по SMS:</div>
          <div class="d-flex flex-wrap justify-content-center gap-2">
            {#each phones as ph (ph.id)}
              <button class="btn btn-sm btn-outline-secondary" disabled={busy} onclick={() => sms(ph.id)}>
                <i class="bi bi-chat-dots me-1"></i>{ph.numberWithDialCode}
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </div>
  </div>
</div>
