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
  // 归档成员(尤其 zip 无日期字段的合成目录)可能带零时间(0001-01-01),
  // 以及 zip DOS 日期最早为 1980:早于 1980 视为无日期。
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1980) return ''
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}
