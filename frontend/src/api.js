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

// Raw URL mirrors the file's path, so a document's relative resources
// resolve against its own directory: /api/raw/dir/page.html loads
// ./img.png as /api/raw/dir/img.png.
export function rawUrl(path) {
  return '/api/raw/' + path.split('/').filter(Boolean).map(encodeURIComponent).join('/')
}

export function downloadUrl(path) {
  return `/api/download?path=${encodeURIComponent(path)}`
}
