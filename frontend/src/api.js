export async function listDir(path) {
  const query = !path || path === '/' ? '' : `?path=${encodeURIComponent(path)}`
  const res = await fetch(`/api/list${query}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

export function fileUrl(path) {
  return `/api/file?path=${encodeURIComponent(path)}`
}
