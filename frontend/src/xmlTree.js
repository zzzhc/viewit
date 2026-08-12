// Pure logic for the XML viewer: DOM parsing, source-offset mapping,
// collapsible tree rows, and XPath evaluation. No UI code here.

export const MAX_TREE_ROWS = 50000
const MAX_LABEL = 80

// Parse text as XML. Returns { ok: true, doc } or { ok: false, error }.
function parseXml(text) {
  const doc = new DOMParser().parseFromString(text, 'application/xml')
  const err = doc.querySelector('parsererror')
  if (err) {
    return { ok: false, error: (err.textContent || 'XML 解析失败').replace(/\s+/g, ' ').trim().slice(0, 300) }
  }
  return { ok: true, doc }
}

// Returns { ok, doc, rows, offsets, treeDisabled }.
//  - rows: flat tree rows [{ id, kind, depth, ancestors, node, label, title, hasChildren, range }]
//          kind: 'element' | 'attr' | 'text' | 'comment' | 'cdata'; ancestors = 祖先行 id 数组
//  - offsets: Map<Element, { start, openEnd, end }>，end = 闭合标签后的偏移（自闭合时 = openEnd）
//  - treeDisabled: 行数超过 MAX_TREE_ROWS（rows 为 null，仅显示源码）
export function analyze(text) {
  const { ok, doc, error } = parseXml(text)
  if (!ok) return { ok, error }

  // --- 引号感知的标签扫描器（跳过注释/CDATA/PI/doctype，只产出元素开闭标签事件）---
  const events = []
  let i = 0
  const n = text.length
  while (i < n) {
    const lt = text.indexOf('<', i)
    if (lt === -1) break
    const c = text[lt + 1]
    if (c === '!') {
      if (text.startsWith('<!--', lt)) i = text.indexOf('-->', lt + 4) + 3
      else if (text.startsWith('<![CDATA[', lt)) i = text.indexOf(']]>', lt + 9) + 3
      else i = text.indexOf('>', lt) + 1 // doctype（含内部子集的文档类型声明偏移可能不准，但只影响注释/PI 之后的元素定位起点，可接受）
      if (i < 0) break
      continue
    }
    if (c === '?') { // PI / xml 声明
      i = text.indexOf('?>', lt) + 2
      if (i <= 1) break
      continue
    }
    if (c === '/') { // 闭合标签
      let j = lt + 2
      while (j < n && !/[\s>]/.test(text[j])) j++
      const end = text.indexOf('>', j) + 1
      if (end <= 0) break
      events.push({ kind: 'close', start: lt, end })
      i = end
      continue
    }
    // 开始标签：引号感知直到 '>'
    let j = lt + 1
    while (j < n && !/[\s/>]/.test(text[j])) j++
    let k = j
    let quote = ''
    while (k < n) {
      const ch = text[k]
      if (quote) { if (ch === quote) quote = '' }
      else if (ch === '"' || ch === "'") quote = ch
      else if (ch === '>') break
      k++
    }
    if (k >= n) break
    events.push({ kind: 'open', start: lt, end: k + 1, selfClosing: /\/\s*$/.test(text.slice(j, k)) })
    i = k + 1
  }

  // --- 事件与 DOM 元素按文档序配对（良构 XML 中：开标签顺序 == 元素前序；闭标签用栈配对）---
  const offsets = new Map()
  const els = []
  ;(function walk(node) {
    els.push(node)
    for (const child of node.children) walk(child)
  })(doc.documentElement)
  const stack = []
  let ei = 0
  for (const ev of events) {
    if (ev.kind === 'open') {
      const el = els[ei++]
      if (el) {
        offsets.set(el, { start: ev.start, openEnd: ev.end, end: ev.selfClosing ? ev.end : -1 })
        if (!ev.selfClosing) stack.push(el)
      }
    } else {
      const el = stack.pop()
      if (el) offsets.get(el).end = ev.end
    }
  }

  // --- 扁平树行（DFS；行 id 恒等于 rows.length，0 起）---
  const rows = []
  function pushRow(kind, depth, ancestors, node, label, title, hasChildren, range) {
    rows.push({ id: rows.length, kind, depth, ancestors, node, label, title, hasChildren, range })
  }
  function rangeFor(node) {
    const o = offsets.get(node)
    if (!o) return null
    return [o.start, o.end === -1 ? o.openEnd : o.end]
  }
  function attrRange(el, attrName) {
    const o = offsets.get(el)
    if (!o) return null
    const open = text.slice(o.start, o.openEnd)
    const m = open.match(new RegExp('\\b' + attrName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*='))
    if (!m || m.index === undefined) return null
    const from = o.start + m.index
    return [from, from + attrName.length]
  }
  const truncate = (s) => (s.length > MAX_LABEL ? s.slice(0, MAX_LABEL) + '…' : s)

  function build(node, depth, ancestors) {
    const rowId = rows.length
    pushRow('element', depth, ancestors, node, `<${node.nodeName}>`, '', true, rangeFor(node))
    const childAncestors = ancestors.concat(rowId)
    const range = rangeFor(node)
    for (const attr of node.attributes) {
      pushRow('attr', depth + 1, childAncestors, attr, `@${attr.name}="${truncate(attr.value)}"`, `${attr.name}="${attr.value}"`, false, attrRange(node, attr.name) || range)
    }
    for (const child of node.childNodes) {
      if (child.nodeType === 3) { // Text
        if (!child.data.trim()) continue // 纯缩进空白行不建树行
        pushRow('text', depth + 1, childAncestors, child, `"${truncate(child.data)}"`, child.data, false, range)
      } else if (child.nodeType === 8) { // Comment
        pushRow('comment', depth + 1, childAncestors, child, `<!--${truncate(child.data)}-->`, `<!--${child.data}-->`, false, range)
      } else if (child.nodeType === 4) { // CDATA
        pushRow('cdata', depth + 1, childAncestors, child, `<![CDATA[${truncate(child.data)}]]>`, `<![CDATA[${child.data}]]>`, false, range)
      } else if (child.nodeType === 1) { // Element
        build(child, depth + 1, childAncestors)
      }
    }
  }
  build(doc.documentElement, 0, [])

  if (rows.length > MAX_TREE_ROWS) return { ok: true, doc, rows: null, offsets, treeDisabled: true }
  return { ok: true, doc, rows, offsets, treeDisabled: false }
}

const NAME_RE = '[A-Za-z_][\\w.-]*'

// 文档存在默认命名空间时的兜底改写：把无前缀元素名步骤改写为 *[local-name()='name']，
// 使 //patent 在 xmlns 文档里仍能命中。非元素步骤（@attr、text()、node()、*、轴::、带前缀名）不受影响。
export function rewriteNoNs(xpath) {
  return xpath.replace(
    new RegExp('(^|/|::)(' + NAME_RE + ')(?!\\s*\\(|\\s*:)(?=\\s*(\\[[^\\]]*\\]\\s*)?(/|$))', 'g'),
    (m, pre, name) => pre + "*[local-name()='" + name + "']"
  )
}

// ANY_TYPE 求值结果可能是快照、迭代器或单节点类型（Chrome 对节点集默认返回迭代器），统一收集为数组。
function collectNodes(res) {
  const t = res.resultType
  if (t === XPathResult.UNORDERED_NODE_SNAPSHOT_TYPE || t === XPathResult.ORDERED_NODE_SNAPSHOT_TYPE) {
    const nodes = []
    for (let idx = 0; idx < res.snapshotLength; idx++) nodes.push(res.snapshotItem(idx))
    return nodes
  }
  if (t === XPathResult.UNORDERED_NODE_ITERATOR_TYPE || t === XPathResult.ORDERED_NODE_ITERATOR_TYPE) {
    const nodes = []
    let node
    while ((node = res.iterateNext())) nodes.push(node)
    return nodes
  }
  if (t === XPathResult.ANY_UNORDERED_NODE_TYPE || t === XPathResult.FIRST_ORDERED_NODE_TYPE) {
    return res.singleNodeValue ? [res.singleNodeValue] : []
  }
  return []
}

// 求值 XPath。返回 { kind: 'nodes', nodes, ranges } | { kind: 'scalar', value } | { kind: 'error', message }
export function evalXPath(xpath, doc, offsets, text) {
  const resolver = (prefix) => {
    if (!prefix) return doc.lookupNamespaceURI(null) || null
    return doc.lookupNamespaceURI(prefix) || null
  }
  let res
  try {
    res = doc.evaluate(xpath, doc, resolver, XPathResult.ANY_TYPE, null)
  } catch (e) {
    return { kind: 'error', message: e.message || 'XPath 无效' }
  }
  if (res.resultType === XPathResult.NUMBER_TYPE) return { kind: 'scalar', value: String(res.numberValue) }
  if (res.resultType === XPathResult.STRING_TYPE) return { kind: 'scalar', value: res.stringValue }
  if (res.resultType === XPathResult.BOOLEAN_TYPE) return { kind: 'scalar', value: String(res.booleanValue) }
  let nodes = collectNodes(res)
  // 空结果 + 存在默认命名空间 → 用 local-name 改写重试一次
  if (nodes.length === 0 && doc.lookupNamespaceURI(null)) {
    try {
      res = doc.evaluate(rewriteNoNs(xpath), doc, resolver, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null)
      nodes = collectNodes(res)
    } catch { /* 保持空结果 */ }
  }
  const ranges = nodes.map((node) => rangeForNode(node, offsets, text)).filter(Boolean)
  return { kind: 'nodes', nodes, ranges }
}

function rangeForNode(node, offsets, text) {
  if (node.nodeType === 1) { // Element
    const o = offsets.get(node)
    return o ? [o.start, o.end === -1 ? o.openEnd : o.end] : null
  }
  if (node.nodeType === 2) { // Attr：在开标签文本内定位 name=
    const o = offsets.get(node.ownerElement)
    if (!o) return null
    const open = text.slice(o.start, o.openEnd)
    const m = open.match(new RegExp('\\b' + node.name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*='))
    return m && m.index !== undefined ? [o.start + m.index, o.start + m.index + node.name.length] : [o.start, o.start]
  }
  // Text(3)/Comment(8)/CDATA(4)：回退到父元素区间
  if (node.parentElement) {
    const o = offsets.get(node.parentElement)
    return o ? [o.start, o.end === -1 ? o.openEnd : o.end] : null
  }
  return null
}

const INDENT = '  '

function escapeText(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;')
}
function escapeAttr(s) {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/"/g, '&quot;')
    .replace(/\t/g, '&#9;')
    .replace(/\n/g, '&#10;')
    .replace(/\r/g, '&#13;')
}

// 格式化 XML：统一缩进与换行，保留声明/DOCTYPE/注释/CDATA/PI/文本内容原样。
// 混合内容（文本与子元素并存）不重排，保持原始空白。
// 返回 { ok: true, text } 或 { ok: false, error }（解析失败）。
export function formatXml(text) {
  const { ok, doc, error } = parseXml(text)
  if (!ok) return { ok, error }
  const parts = []
  let from = 0
  // 提取 <?xml ...?> 声明（仅当位于文件开头，且不是 <?xml-stylesheet 之类的 PI）
  const decl = /^\s*<\?xml(?=[\s?])[\s\S]*?\?>\s*/.exec(text)
  if (decl) {
    parts.push(decl[0].trimEnd() + '\n')
    from = decl[0].length
  }
  // 提取 DOCTYPE（跳过 [ ] 内部子集内的 '>'）
  const dt = /<!DOCTYPE/i.exec(text.slice(from))
  if (dt) {
    const s = from + dt.index
    let i = s
    let bracket = 0
    while (i < text.length) {
      const ch = text[i]
      if (ch === '[') bracket++
      else if (ch === ']') bracket--
      else if (ch === '>' && bracket <= 0) break
      i++
    }
    if (i < text.length) {
      parts.push(text.slice(s, i + 1) + '\n')
      from = i + 1
    }
  }
  const serializer = new XMLSerializer()
  const out = []
  ;(function emit(node, depth) {
    const ind = INDENT.repeat(depth)
    switch (node.nodeType) {
      case 1: { // Element
        const attrs = Array.from(node.attributes, (a) => `${a.name}="${escapeAttr(a.value)}"`)
        const open = `<${node.nodeName}${attrs.length ? ' ' + attrs.join(' ') : ''}>`
        const children = Array.from(node.childNodes)
        if (!children.length) {
          out.push(ind + open.slice(0, -1) + '/>')
          return
        }
        if (children.some((c) => c.nodeType === 3 && c.data.trim() !== '')) {
          // 文本与子元素并存：保持内部原样（含原始空白），整体按当前缩进对齐
          out.push(ind + serializer.serializeToString(node))
          return
        }
        out.push(ind + open)
        for (const c of children) emit(c, depth + 1)
        out.push(ind + `</${node.nodeName}>`)
        return
      }
      case 3: { // Text
        if (!node.data.trim()) return // 纯空白由缩进控制
        const lines = node.data.split('\n')
        out.push(ind + escapeText(lines[0]))
        for (let i = 1; i < lines.length; i++) out.push(escapeText(lines[i]))
        return
      }
      case 8: // Comment
        out.push(ind + `<!--${node.data}-->`)
        return
      case 4: // CDATA
        out.push(ind + `<![CDATA[${node.data}]]>`)
        return
      case 7: // PI
        out.push(ind + `<?${node.target}${node.data ? ' ' + node.data : ''}?>`)
        return
      default:
        return
    }
  })(doc.documentElement, 0)
  return { ok: true, text: parts.join('') + out.join('\n') + '\n' }
}
