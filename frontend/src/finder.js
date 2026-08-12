// finder.js — 文件模糊查找:WebSocket 客户端与查询状态(逻辑层,UI 在 FileFinder.svelte)
// 协议:发送 {"q": "查询词", "path": "当前目录"},接收
// {"type":"results","partial","indexed","matched","truncated","matches"}
// matches[].marks 是命中的 rune 偏移(服务端由 sahilm/fuzzy 计算),用于高亮。

const listeners = new Set()
let ws = null
let latestQ = ''
let latestScope = ''
let partialTimer = 0

function wsURL() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${location.host}/api/ws`
}

function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return
  ws = new WebSocket(wsURL())
  ws.onopen = () => send()
  ws.onmessage = (ev) => {
    let msg
    try {
      msg = JSON.parse(ev.data)
    } catch {
      return
    }
    if (!msg || msg.type !== 'results') return
    for (const fn of listeners) fn(msg)
    // 索引未完成时稍后重发,直到拿到全量结果
    if (msg.partial && latestQ) {
      clearTimeout(partialTimer)
      partialTimer = setTimeout(send, 400)
    }
  }
  ws.onclose = () => {
    ws = null
  }
}

function send() {
  if (!ws || ws.readyState !== WebSocket.OPEN) return
  ws.send(JSON.stringify({ q: latestQ, path: latestScope }))
}

// setQuery 发送新查询(空串也发送,用于获取索引进度;同时取消未决的重发)。
// scope 为当前目录("/" 或 "docs" 等),限制查找范围;不传则全量。
export function setQuery(q, scope) {
  latestQ = q
  latestScope = scope || ''
  clearTimeout(partialTimer)
  connect()
  send()
}

export function subscribe(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

// highlightChunks 把 path 按「目录/文件名 × 是否命中」拆成渲染块:
// {t: 文本, hit: 是否命中高亮, dir: 是否目录部分}。marks 为 rune 偏移。
export function highlightChunks(path, marks) {
  const chars = Array.from(path)
  const hit = new Array(chars.length).fill(false)
  for (const m of marks) {
    if (m >= 0 && m < hit.length) hit[m] = true
  }
  let lastSlash = -1
  for (let i = 0; i < chars.length; i++) {
    if (chars[i] === '/') lastSlash = i
  }
  const nameStart = lastSlash + 1
  const out = []
  let cur = ''
  let curHit = false
  let curDir = true
  for (let i = 0; i < chars.length; i++) {
    const dir = i < nameStart
    const h = hit[i]
    if (cur && (h !== curHit || dir !== curDir)) {
      out.push({ t: cur, hit: curHit, dir: curDir })
      cur = ''
    }
    curHit = h
    curDir = dir
    cur += chars[i]
  }
  if (cur) out.push({ t: cur, hit: curHit, dir: curDir })
  return out
}
