const UNITS = ['B', 'KB', 'MB', 'GB', 'TB']

export function formatSize(bytes) {
  let v = bytes
  let u = 0
  while (v >= 1024 && u < UNITS.length - 1) {
    v /= 1024
    u++
  }
  return (u === 0 ? `${v} ` : `${v.toFixed(1)} `) + UNITS[u]
}

export function formatDate(iso) {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
