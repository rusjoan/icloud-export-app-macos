// Токен приходит в URL (?t=...) — без него API не отвечает.
const token = new URLSearchParams(location.search).get('t') || ''

export async function api(path, { method = 'GET', body } = {}) {
  const res = await fetch(path, {
    method,
    headers: { 'X-Token': token, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
  return data
}

export const post = (path, body) => api(path, { method: 'POST', body: body ?? {} })

// Поток состояния с сервера; при обрыве EventSource переподключается сам.
export function subscribe(onState) {
  const es = new EventSource(`/api/events?t=${encodeURIComponent(token)}`)
  es.onmessage = (e) => onState(JSON.parse(e.data))
  return () => es.close()
}

export function formatBytes(n) {
  if (!n) return '0 Б'
  const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ']
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(i ? 1 : 0)} ${units[i]}`
}

export function plural(n, one, few, many) {
  const m10 = n % 10, m100 = n % 100
  if (m10 === 1 && m100 !== 11) return one
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few
  return many
}
