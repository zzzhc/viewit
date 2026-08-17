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

// ---- LevelDB 控制台 ----

// leveldbKeys 分页拉取 key 列表(排他游标):after 为上一页最后一个 key。
// 返回 { keys: string[], hasMore: boolean }。
export async function leveldbKeys(path, { prefix = '', after = '', limit = 500 } = {}) {
  const q = [
    `path=${encodeURIComponent(path)}`,
    `prefix=${encodeURIComponent(prefix)}`,
    `after=${encodeURIComponent(after)}`,
    `limit=${limit}`,
  ].join('&')
  const res = await fetch(`/api/leveldb/keys?${q}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// leveldbGet 返回单个 key 的值:{key,size,text?|base64?|tooBig?}。
export async function leveldbGet(path, key) {
  const res = await fetch(`/api/leveldb/get?path=${encodeURIComponent(path)}&key=${encodeURIComponent(key)}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// leveldbDumpUrl 是流式 NDJSON 导出的下载 URL。
export function leveldbDumpUrl(path, prefix) {
  const q = `path=${encodeURIComponent(path)}&prefix=${encodeURIComponent(prefix)}`
  return `/api/leveldb/dump?${q}`
}

// ldbName 生成导出文件名,与 Go 端 sanitizeName 同规则。
export function ldbName(prefix) {
  return 'dump-' + (prefix.replace(/[^A-Za-z0-9._-]+/g, '_') || 'all') + '.jsonl'
}

// ---- SQLite 查看器 ----

// sqliteTables 返回库中的表/视图列表(含行数与 CREATE 语句)。
// 返回 { tables: [{ name, type, sql, rows }] }。
export async function sqliteTables(path) {
  const res = await fetch(`/api/sqlite/tables?path=${encodeURIComponent(path)}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// sqliteRows 分页拉取表数据:返回 { columns, rows, hasMore, total? }。
// total 仅在 offset=0 时下发(深分页免重复 COUNT)。
export async function sqliteRows(path, table, { offset = 0, limit = 500 } = {}) {
  const q = [
    `path=${encodeURIComponent(path)}`,
    `table=${encodeURIComponent(table)}`,
    `offset=${offset}`,
    `limit=${limit}`,
  ].join('&')
  const res = await fetch(`/api/sqlite/rows?${q}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// sqliteQuery 执行只读 SQL:返回 { columns, rows, truncated }。
// 写语句被服务端(query_only)拒绝并抛错。
export async function sqliteQuery(path, sql, { limit = 500 } = {}) {
  const q = `path=${encodeURIComponent(path)}&sql=${encodeURIComponent(sql)}&limit=${limit}`
  const res = await fetch(`/api/sqlite/query?${q}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// sqliteExportUrl 是流式导出(CSV/JSONL)的下载 URL:table 或 sql 二选一。
// sql 模式导出完整查询结果(不受 query 接口的行数截断限制)。
export function sqliteExportUrl(path, { table = '', sql = '', format = 'csv' } = {}) {
  const q = [`path=${encodeURIComponent(path)}`, `format=${format}`]
  if (table) q.push(`table=${encodeURIComponent(table)}`)
  if (sql) q.push(`sql=${encodeURIComponent(sql)}`)
  return `/api/sqlite/export?${q.join('&')}`
}

// ---- Parquet 查看器 ----

// parquetMeta 返回 schema 与行数:
// { columns: [{ name, type, repetition }], rows, createdBy?, rowGroups }。
export async function parquetMeta(path) {
  const res = await fetch(`/api/parquet/meta?path=${encodeURIComponent(path)}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// parquetRows 分页拉取行数据:返回 { columns, rows, hasMore, total? }。
// total 仅在 offset=0 时下发(有过滤时仅扫完全文件才有匹配总数)。
// filters: [{ col, op, val }],col 空表示任意列。
export async function parquetRows(path, { offset = 0, limit = 500, filters = [] } = {}) {
  const q = [
    `path=${encodeURIComponent(path)}`,
    `offset=${offset}`,
    `limit=${limit}`,
  ]
  for (const f of filters) {
    q.push(`f=${encodeURIComponent(`${f.op}:${f.col}=${f.val}`)}`)
  }
  const res = await fetch(`/api/parquet/rows?${q.join('&')}`)
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || `HTTP ${res.status}`)
  }
  return data
}

// parquetExportUrl 是流式导出(CSV/JSONL)的下载 URL;filters 与 rows 接口同形。
export function parquetExportUrl(path, format = 'csv', filters = []) {
  const q = [`path=${encodeURIComponent(path)}`, `format=${format}`]
  for (const f of filters) {
    q.push(`f=${encodeURIComponent(`${f.op}:${f.col}=${f.val}`)}`)
  }
  return `/api/parquet/export?${q.join('&')}`
}
