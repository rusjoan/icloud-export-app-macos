<script>
  import { post } from './api.js'

  let { ex } = $props()
  let error = $state('')

  async function install() {
    error = ''
    try {
      await post('/api/exiftool/install')
    } catch (e) {
      error = e.message
    }
  }
</script>

<div class="card shadow-sm mb-4">
  <div class="card-body">
    <div class="d-flex align-items-start gap-3">
      <i class="bi {ex.path ? 'bi-check-circle-fill text-success' : 'bi-exclamation-circle text-warning'} fs-4"></i>
      <div class="flex-grow-1">
        <div class="fw-semibold">Метаданные</div>
        {#if ex.path}
          <div class="small text-body-secondary">
            exiftool {ex.version} найден — дата, место, подпись, избранное и альбомы записываются прямо в файлы.
            <span class="mono selectable">{ex.path}</span>
          </div>
        {:else}
          <div class="small text-body-secondary mb-2">
            exiftool не найден. Выгрузка будет работать, но метаданные из iCloud лягут рядом с файлами в XMP-сайдкарах
            (<span class="mono">IMG_1234.HEIC.xmp</span>), а не внутрь файлов. Даты файлов выставляются в любом случае.
          </div>
          {#if ex.installing}
            <div class="small"><span class="spinner-border spinner-border-sm me-2"></span>Устанавливаю через Homebrew…</div>
          {:else if ex.brew}
            <button class="btn btn-sm btn-outline-primary" onclick={install}>
              <i class="bi bi-box-arrow-in-down me-1"></i>Установить через Homebrew
            </button>
          {:else}
            <div class="small">
              Установите <a href="https://brew.sh" target="_blank" rel="noreferrer">Homebrew</a>, затем в Терминале:
              <span class="mono selectable">brew install exiftool</span> — и перезапустите приложение.
            </div>
          {/if}
        {/if}
        {#if error || ex.error}<div class="alert alert-danger small py-2 mt-2 selectable">{error || ex.error}</div>{/if}
        {#if ex.log?.length && (ex.installing || ex.error)}
          <pre class="log-box mono bg-body-tertiary p-2 rounded mt-2 mb-0 selectable">{ex.log.join('\n')}</pre>
        {/if}
      </div>
    </div>
  </div>
</div>
