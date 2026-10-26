<script>
  import { onMount } from 'svelte'
  import { api, post, plural } from './api.js'
  import ExifTool from './ExifTool.svelte'

  let { st } = $props()

  const PREFS_KEY = 'icloud-export-prefs'
  let prefs = {}
  try {
    prefs = JSON.parse(localStorage.getItem(PREFS_KEY) || '{}')
  } catch {}

  let albums = $state([])
  let loading = $state(true)
  let loadError = $state('')
  let selected = $state(new Set(prefs.selected || []))
  let dest = $state(prefs.dest || '~/Pictures/iCloud Export')
  let layout = $state(prefs.layout || 'date')
  let concurrency = $state(prefs.concurrency || 4)
  let destInfo = $state(null)
  let starting = $state(false)
  let startError = $state('')

  async function load(refresh = false) {
    loading = true
    loadError = ''
    try {
      albums = await api('/api/albums' + (refresh ? '?refresh=1' : ''))
      const ids = new Set(albums.map((a) => a.id))
      selected = new Set([...selected].filter((id) => ids.has(id)))
    } catch (e) {
      loadError = e.message
    } finally {
      loading = false
    }
  }
  onMount(() => load())

  const groups = $derived.by(() => {
    const own = albums.filter((a) => !a.shared)
    const smart = own.filter((a) => a.kind === 'smart')
    const special = own.filter((a) => a.kind === 'hidden' || a.kind === 'deleted')
    const user = own.filter((a) => a.kind === 'user' || a.kind === 'folder')
    const shared = albums.filter((a) => a.shared)
    // дерево пользовательских альбомов: папки и вложенность
    const children = new Map()
    for (const a of user) {
      const p = user.some((x) => x.id === a.parentId) ? a.parentId : ''
      if (!children.has(p)) children.set(p, [])
      children.get(p).push(a)
    }
    const tree = []
    const walk = (parent, depth) => {
      for (const a of children.get(parent) || []) {
        tree.push({ ...a, depth })
        walk(a.id, depth + 1)
      }
    }
    walk('', 0)
    return { smart, special, user: tree, shared }
  })

  const selectable = $derived(albums.filter((a) => a.kind !== 'folder'))
  const selectedCount = $derived(selectable.filter((a) => selected.has(a.id)).reduce((n, a) => n + a.count, 0))

  function toggle(id) {
    const s = new Set(selected)
    s.has(id) ? s.delete(id) : s.add(id)
    selected = s
  }
  function setAll(list, on) {
    const s = new Set(selected)
    for (const a of list) if (a.kind !== 'folder') on ? s.add(a.id) : s.delete(a.id)
    selected = s
  }

  async function pick() {
    try {
      const r = await post('/api/pick-folder')
      if (r.path) dest = r.path
    } catch (e) {
      startError = e.message
    }
  }

  // есть ли в папке прошлая выгрузка и с какой раскладкой
  let destTimer
  $effect(() => {
    const d = dest
    clearTimeout(destTimer)
    destTimer = setTimeout(async () => {
      try {
        destInfo = await api('/api/dest?path=' + encodeURIComponent(d))
      } catch {
        destInfo = null
      }
    }, 300)
  })

  const layoutName = { flat: 'в одну папку', date: 'по датам' }

  async function start() {
    starting = true
    startError = ''
    const ids = selectable.filter((a) => selected.has(a.id)).map((a) => a.id)
    try {
      localStorage.setItem(PREFS_KEY, JSON.stringify({ selected: ids, dest, layout, concurrency }))
    } catch {}
    try {
      await post('/api/export', { albums: ids, dest, layout, concurrency: Number(concurrency) })
    } catch (e) {
      startError = e.message
    } finally {
      starting = false
    }
  }
</script>

{#snippet albumRow(a)}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div class="album-row d-flex align-items-center px-3 py-2 border-bottom" style="padding-left: {1 + (a.depth || 0) * 1.5}rem !important"
    onclick={() => a.kind !== 'folder' && toggle(a.id)}>
    {#if a.kind === 'folder'}
      <i class="bi bi-folder2 me-2 text-body-secondary"></i>
      <span class="text-body-secondary">{a.name}</span>
    {:else}
      <input class="form-check-input me-2 mt-0" type="checkbox" checked={selected.has(a.id)} onclick={(e) => e.stopPropagation()}
        onchange={() => toggle(a.id)} />
      <i class="bi {a.kind === 'hidden' ? 'bi-eye-slash' : a.kind === 'deleted' ? 'bi-trash3' : a.shared ? 'bi-people' : a.kind === 'smart' ? 'bi-images' : 'bi-collection'} me-2 text-body-secondary"></i>
      <span>{a.name}</span>
      <span class="ms-auto badge text-bg-light border">{a.count}</span>
    {/if}
  </div>
{/snippet}

{#snippet group(title, list)}
  {#if list.length}
    <div class="d-flex align-items-center px-3 py-1 bg-body-tertiary border-bottom small text-uppercase text-body-secondary">
      <span>{title}</span>
      <button class="btn btn-link btn-sm ms-auto p-0 me-2" onclick={() => setAll(list, true)}>все</button>
      <button class="btn btn-link btn-sm p-0" onclick={() => setAll(list, false)}>ни одного</button>
    </div>
    {#each list as a (a.id)}{@render albumRow(a)}{/each}
  {/if}
{/snippet}

<div class="card shadow-sm mb-4">
  <div class="card-header d-flex align-items-center bg-body">
    <span class="fw-semibold">Альбомы</span>
    <button class="btn btn-sm btn-outline-secondary ms-auto" onclick={() => load(true)} disabled={loading} title="Обновить">
      <i class="bi bi-arrow-clockwise"></i>
    </button>
  </div>
  {#if loading}
    <div class="text-center text-body-secondary py-5"><div class="spinner-border"></div><div class="mt-2">Загружаю альбомы…</div></div>
  {:else if loadError}
    <div class="alert alert-danger m-3 selectable">{loadError}</div>
  {:else}
    <div class="album-list">
      {@render group('Медиатека', groups.smart)}
      {@render group('Скрытые и удалённые', groups.special)}
      {@render group('Мои альбомы', groups.user)}
      {@render group('Общая медиатека', groups.shared)}
    </div>
    <div class="card-footer small text-body-secondary bg-body">
      Выбрано: {selectable.filter((a) => selected.has(a.id)).length} {plural(selectable.filter((a) => selected.has(a.id)).length, 'альбом', 'альбома', 'альбомов')},
      до {selectedCount} {plural(selectedCount, 'файла', 'файлов', 'файлов')} (файл из нескольких альбомов скачивается один раз)
    </div>
  {/if}
</div>

<div class="card shadow-sm mb-4">
  <div class="card-body">
    <label class="form-label fw-semibold" for="dest">Куда сохранить</label>
    <div class="input-group mb-2">
      <input id="dest" class="form-control" bind:value={dest} placeholder="/Users/…/Pictures/iCloud" />
      {#if st.nativePicker}
        <button class="btn btn-outline-secondary" onclick={pick}><i class="bi bi-folder2-open me-1"></i>Выбрать…</button>
      {/if}
    </div>
    {#if destInfo?.layout}
      <div class="small mb-2 {destInfo.layout !== layout ? 'text-warning-emphasis' : 'text-success'}">
        <i class="bi bi-arrow-repeat me-1"></i>
        В папке уже есть выгрузка ({layoutName[destInfo.layout]}) — будут докачаны только новые и изменённые файлы.
        {#if destInfo.layout !== layout}
          Раскладка отличается: уже скачанные файлы останутся на своих местах, новые лягут {layoutName[layout]}.
        {/if}
      </div>
    {/if}

    <div class="row g-3 mt-1">
      <div class="col-sm-8">
        <div class="form-label fw-semibold">Раскладка</div>
        <div class="form-check">
          <input class="form-check-input" type="radio" id="l-date" value="date" bind:group={layout} />
          <label class="form-check-label" for="l-date">По датам — <span class="mono">2024/06/2024-06-10 21.30.05 IMG_1234.HEIC</span></label>
        </div>
        <div class="form-check">
          <input class="form-check-input" type="radio" id="l-flat" value="flat" bind:group={layout} />
          <label class="form-check-label" for="l-flat">Всё в одну папку — <span class="mono">2024-06-10 21.30.05 IMG_1234.HEIC</span></label>
        </div>
      </div>
      <div class="col-sm-4">
        <label class="form-label fw-semibold" for="conc">Параллельных загрузок</label>
        <select id="conc" class="form-select" bind:value={concurrency}>
          {#each [1, 2, 4, 6, 8, 12, 16] as n}<option value={n}>{n}</option>{/each}
        </select>
      </div>
    </div>
  </div>
</div>

<ExifTool ex={st.exiftool} os={st.os} />

{#if startError}<div class="alert alert-danger selectable">{startError}</div>{/if}

<div class="d-flex justify-content-end mb-5">
  <button class="btn btn-primary btn-lg px-4" onclick={start} disabled={starting || loading || !dest.trim() || !selectable.some((a) => selected.has(a.id))}>
    {#if starting}<span class="spinner-border spinner-border-sm me-2"></span>{/if}
    <i class="bi bi-download me-2"></i>Выгрузить
  </button>
</div>
