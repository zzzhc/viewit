// 目录列表分页的页大小:虚拟滚动按需拉取,每页 stat/传输/解析只承担
// 一页的成本,大目录(数十万条目)下首页秒开。
export const LIST_CHUNK = 2000

export async function listDir(path, { offset, limit } = {}) {
  const q = []
  if (path && path !== '/') q.push(`path=${encodeURIComponent(path)}`)
  if (offset) q.push(`offset=${offset}`)
  if (limit) q.push(`limit=${limit}`)
  const query = q.length ? `?${q.join('&')}` : ''
  const t0 = performance.now()
  const res = await fetch(`/api/list${query}`)
  const t1 = performance.now()
  const data = await res.json().catch(() => null)
  const t2 = performance.now()
  console.log(
    `[perf] listDir(${path || '/'}${offset ? `+${offset}` : ''}) fetch=${Math.round(t1 - t0)}ms parse=${Math.round(t2 - t1)}ms wire=${res.headers.get('content-length') ?? '?'}B`
  )
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

export function fileUrl(path) {
  return `/api/file?path=${encodeURIComponent(path)}`
}

// 目录下图片文件名列表(与目录列表同序),供图片查看器上一张/下一张切换。
// 服务端按 images=1 只返回名字、不做分页,大目录下成本远低于全量列表。
export async function listImages(path) {
  const q = `?images=1${path && path !== '/' ? '&path=' + encodeURIComponent(path) : ''}`
  const res = await fetch(`/api/list${q}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data.images || []
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
