<script>
  import { post, formatBytes } from './api.js'

  let { p } = $props()
  let error = $state('')

  // скорость — скользящее среднее по приращению байт
  let speed = $state(0)
  let last = { t: 0, b: 0 }
  $effect(() => {
    const now = performance.now()
    if (last.t && p.phase === 'download') {
      const dt = (now - last.t) / 1000
      if (dt > 0.2) {
        const inst = Math.max(0, (p.bytes - last.b) / dt)
        speed = speed ? speed * 0.8 + inst * 0.2 : inst
        last = { t: now, b: p.bytes }
      }
    } else {
      last = { t: now, b: p.bytes }
    }
  })

  const active = $derived(['scan', 'download', 'paused'].includes(p.phase))
  const pct = $derived(p.totalBytes ? Math.min(100, (p.bytes / p.totalBytes) * 100) : p.total ? (p.done / p.total) * 100 : 0)
  const eta = $derived.by(() => {
    if (p.phase !== 'download' || speed < 1) return ''
    const s = (p.totalBytes - p.bytes) / speed
    if (!isFinite(s) || s <= 0) return ''
    if (s < 60) return `${Math.round(s)} с`
    if (s < 3600) return `${Math.round(s / 60)} мин`
    return `${Math.floor(s / 3600)} ч ${Math.round((s % 3600) / 60)} мин`
  })

  const title = {
    scan: 'Собираю список файлов…',
    download: 'Выгрузка',
    paused: 'Пауза',
    done: 'Готово',
    cancelled: 'Остановлено',
    failed: 'Ошибка',
  }

  async function act(action) {
    error = ''
    try {
      await post('/api/export/' + action)
    } catch (e) {
      error = e.message
    }
  }
  async function reveal() {
    try {
      await post('/api/reveal')
    } catch (e) {
      error = e.message
    }
  }
</script>

<div class="card shadow-sm mb-4">
  <div class="card-body p-4">
    <div class="d-flex align-items-center mb-3">
      <h5 class="mb-0">
        {#if p.phase === 'done'}<i class="bi bi-check-circle-fill text-success me-2"></i>
        {:else if p.phase === 'failed'}<i class="bi bi-x-circle-fill text-danger me-2"></i>
        {:else if p.phase === 'paused'}<i class="bi bi-pause-circle text-warning me-2"></i>
        {:else if p.phase === 'cancelled'}<i class="bi bi-stop-circle text-secondary me-2"></i>
        {:else}<span class="spinner-border spinner-border-sm text-primary me-2"></span>{/if}
        {title[p.phase] || p.phase}
      </h5>
      <div class="ms-auto d-flex gap-2">
        {#if p.phase === 'paused'}
          <button class="btn btn-outline-primary" onclick={() => act('resume')}><i class="bi bi-play-fill me-1"></i>Продолжить</button>
        {:else if active}
          <button class="btn btn-outline-secondary" onclick={() => act('pause')}><i class="bi bi-pause-fill me-1"></i>Пауза</button>
        {/if}
        {#if active}
          <button class="btn btn-outline-danger" onclick={() => act('cancel')}><i class="bi bi-stop-fill me-1"></i>Остановить</button>
        {:else}
          <button class="btn btn-outline-secondary" onclick={reveal}><i class="bi bi-folder2-open me-1"></i>Открыть папку</button>
          <button class="btn btn-primary" onclick={() => act('clear')}>Новая выгрузка</button>
        {/if}
      </div>
    </div>

    <div class="small text-body-secondary mb-2 selectable mono">{p.dest}</div>

    {#if p.phase === 'scan'}
      <div class="text-body-secondary">Найдено файлов: <b>{p.scanned}</b></div>
    {:else}
      <div class="progress mb-2" style="height: 1.25rem">
        <div class="progress-bar {active && p.phase !== 'paused' ? 'progress-bar-striped progress-bar-animated' : ''} {p.phase === 'done' ? 'bg-success' : ''}"
          style="width: {pct}%"></div>
      </div>
      <div class="d-flex flex-wrap gap-3 small">
        <span><b>{p.done}</b> из <b>{p.total}</b> файлов</span>
        <span>{formatBytes(p.bytes)} из {formatBytes(p.totalBytes)}</span>
        {#if p.phase === 'download' && speed}<span>{formatBytes(speed)}/с</span>{/if}
        {#if eta}<span>осталось ≈ {eta}</span>{/if}
      </div>
      <div class="d-flex flex-wrap gap-2 mt-3">
        <span class="badge text-bg-primary">скачано {p.downloaded}</span>
        <span class="badge text-bg-secondary">уже были {p.skipped}</span>
        {#if p.failed}<span class="badge text-bg-danger">ошибок {p.failed}</span>{/if}
      </div>
    {/if}

    {#if p.message}<div class="alert alert-danger mt-3 mb-0 selectable">{p.message}</div>{/if}
    {#if error}<div class="alert alert-danger mt-3 mb-0 selectable">{error}</div>{/if}
  </div>
</div>

{#if p.current?.length}
  <div class="card shadow-sm mb-4">
    <div class="card-header bg-body small fw-semibold">Сейчас загружаются</div>
    <ul class="list-group list-group-flush">
      {#each p.current as name}
        <li class="list-group-item small mono"><i class="bi bi-arrow-down-circle me-2 text-primary"></i>{name}</li>
      {/each}
    </ul>
  </div>
{/if}

{#if p.errors?.length}
  <div class="card shadow-sm border-danger-subtle mb-4">
    <div class="card-header bg-body small fw-semibold text-danger">
      Ошибки ({p.failed}) — полный список в <span class="mono">.icloud-export-errors.log</span>; при повторном запуске эти файлы будут скачаны снова
    </div>
    <pre class="log-box mono p-3 mb-0 selectable">{p.errors.join('\n')}</pre>
  </div>
{/if}
