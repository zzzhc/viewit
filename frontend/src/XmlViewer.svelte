<script>
  import { fileUrl } from './api.js'
  import { analyze, evalXPath, formatXml, MAX_TREE_ROWS } from './xmlTree.js'

  const MAX_SIZE = 5 * 1024 * 1024

  let { path, name } = $props()

  let text = $state('')
  let tooBig = $state(false)
  let error = $state('')
  let parseError = $state('')
  let doc = $state(null)
  let offsets = $state(null)
  let rows = $state([])
  let treeDisabled = $state(false)
  let expanded = $state(new Set())
  let selected = $state(null)
  let search = $state('')
  let searchState = $state('idle') // 'idle' | 'ok' | 'scalar' | 'error'
  let searchMsg = $state('')
  let matches = $state(new Set()) // 命中行 id
  let highlightRanges = $state([])
  let copied = $state(false)
  let editor = $state(null) // 懒加载模块引用
  let editorPromise = null // 非状态缓存，避免 import 写入 editor 触发 load() 重跑
  let editorApi = $state(null)
  let hostEl = $state(null)

  let visibleRows = $derived(rows.filter((r) => r.depth === 0 || r.ancestors.every((a) => expanded.has(a))))
  let countText = $derived(rows.length ? rows.length + ' 个节点' : '')

  $effect(() => {
    load()
  })

  // 创建/销毁 CodeMirror（text 非空即已成功加载；tooBig/error 时 text 保持 ''，不创建空编辑器）
  $effect(() => {
    if (!editor || !hostEl || !text) return
    const api = editor.createXmlView(hostEl, text)
    editorApi = api
    return () => {
      editorApi = null
      api.destroy()
    }
  })
  $effect(() => {
    if (editorApi) editorApi.setMatches(highlightRanges)
  })

  function ensureEditor() {
    if (!editorPromise) {
      editorPromise = import('./xmlEditor.js').then((m) => {
        editor = m
        return m
      })
    }
    return editorPromise
  }

  async function load() {
    text = ''
    tooBig = false
    error = ''
    parseError = ''
    doc = null
    offsets = null
    rows = []
    treeDisabled = false
    expanded = new Set()
    selected = null
    search = ''
    searchState = 'idle'
    searchMsg = ''
    matches = new Set()
    highlightRanges = []
    copied = false
    try {
      await ensureEditor()
      const res = await fetch(fileUrl(path))
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const len = Number(res.headers.get('content-length') || 0)
      if (len > MAX_SIZE) {
        tooBig = true
        return
      }
      text = await res.text()
      if (text.length > MAX_SIZE) {
        tooBig = true
        return
      }
      const r = analyze(text)
      if (r.ok) {
        doc = r.doc
        offsets = r.offsets
        rows = r.rows || []
        treeDisabled = !r.rows
        if (rows.length) expanded = new Set([rows[0].id]) // 默认展开根，展示第一层
      } else {
        parseError = r.error
      }
    } catch (e) {
      error = e.message
    }
  }

  function toggle(row) {
    const next = new Set(expanded)
    if (next.has(row.id)) next.delete(row.id)
    else next.add(row.id)
    expanded = next
  }

  function expandAll() {
    expanded = new Set(rows.filter((r) => r.hasChildren).map((r) => r.id))
  }

  function collapseAll() {
    expanded = new Set()
  }

  function selectRow(row) {
    selected = row.id
    if (editorApi && row.range) editorApi.reveal(row.range[0], row.range[1])
  }

  function runSearch() {
    const xp = search.trim()
    if (!xp || !doc) return
    const r = evalXPath(xp, doc, offsets, text)
    if (r.kind === 'error') {
      searchState = 'error'
      searchMsg = r.message
      return
    }
    if (r.kind === 'scalar') {
      searchState = 'scalar'
      searchMsg = '结果: ' + r.value
      matches = new Set()
      highlightRanges = []
      return
    }
    const nodeSet = new Set(r.nodes)
    matches = new Set(rows.filter((row) => nodeSet.has(row.node)).map((row) => row.id))
    if (matches.size) {
      // 自动展开命中行的祖先链
      const next = new Set(expanded)
      for (const row of rows) {
        if (nodeSet.has(row.node)) for (const a of row.ancestors) next.add(a)
      }
      expanded = next
    }
    highlightRanges = r.ranges
    searchState = 'ok'
    searchMsg = r.nodes.length ? '找到 ' + r.nodes.length + ' 个节点' : '未找到匹配节点'
  }

  function clearSearch() {
    search = ''
    matches = new Set()
    highlightRanges = []
    searchState = 'idle'
    searchMsg = ''
  }

  // 格式化源码（minified 单行 XML 重排为缩进多行）。文本变化触发 CM 重建，
  // 同时重新 analyze：树行/偏移/XPath 全部基于格式化后的文本保持一致。
  function format() {
    if (!doc) return
    const r = formatXml(text)
    if (!r.ok) {
      parseError = r.error
      return
    }
    if (r.text === text) return
    text = r.text
    const a = analyze(text)
    if (a.ok) {
      parseError = ''
      doc = a.doc
      offsets = a.offsets
      rows = a.rows || []
      treeDisabled = !a.rows
    } else {
      parseError = a.error
    }
    matches = new Set()
    highlightRanges = []
    searchState = 'idle'
    searchMsg = ''
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {
      // clipboard unavailable: fail silently
    }
  }
</script>

<div class="viewer flex flex-col">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      <span class="filetype">XML</span>
    </span>
    <div class="flex min-w-0 flex-1 items-center justify-center gap-1.5 px-3">
      <input class="w-full max-w-[560px] rounded border border-edge bg-bg px-2.5 py-1 font-mono text-[13px] text-fg outline-none focus:border-accent" bind:value={search}
             placeholder="XPath，如 //document-number 或 count(//*)"
             onkeydown={(e) => e.key === 'Enter' && runSearch()} />
      <button class="btn" onclick={runSearch}>搜索</button>
      <button class="btn ghost" onclick={clearSearch}>清除</button>
    </div>
    {#if !tooBig && !error}
      <span class="toolbar-actions">
        <button class="btn" onclick={format} disabled={!doc}>格式化</button>
        <button class="btn" onclick={copy}>{copied ? '已复制' : '复制'}</button>
      </span>
    {/if}
  </div>

  {#if error}
    <div class="hint error">{error}</div>
  {:else if tooBig}
    <div class="hint">
      <p>文件过大(&gt;5MB),请下载查看</p>
      <a class="btn" href={fileUrl(path)} download>下载</a>
    </div>
  {:else}
    <div class="flex flex-none flex-wrap items-center gap-4 border-b border-edge bg-panel px-3 py-1 text-xs text-muted">
      {#if parseError}<span class="text-danger">解析失败：{parseError}</span>{/if}
      {#if searchState === 'ok'}<span class="text-ok">{searchMsg} · {countText}</span>
      {:else if searchState === 'scalar'}<span class="text-ok">{searchMsg}</span>
      {:else if searchState === 'error'}<span class="text-danger">{searchMsg}</span>
      {:else if !parseError}<span>{countText}</span>{/if}
      {#if treeDisabled}<span>节点过多(&gt;{MAX_TREE_ROWS})，已隐藏树形视图</span>{/if}
    </div>
    <div class="flex min-h-0 flex-1">
      {#if !treeDisabled && rows.length}
        <div class="flex w-[320px] min-w-0 flex-none flex-col border-r border-edge bg-panel">
          <div class="flex flex-none gap-2 border-b border-edge px-2 py-1">
            <button class="cursor-pointer border-0 bg-transparent p-0.5 text-xs text-accent hover:underline" onclick={expandAll}>全部展开</button>
            <button class="cursor-pointer border-0 bg-transparent p-0.5 text-xs text-accent hover:underline" onclick={collapseAll}>全部折叠</button>
          </div>
          <div class="flex-1 overflow-auto py-1 pb-3">
            {#each visibleRows as row (row.id)}
              <div class:sel-row={row.id === selected} class:match-row={matches.has(row.id)}
                   class="xml-tree-row" style="padding-left:{row.depth * 14 + 6}px"
                   onclick={() => selectRow(row)}>
                {#if row.hasChildren}
                  <span class="chevron" onclick={(e) => { e.stopPropagation(); toggle(row) }}>
                    {expanded.has(row.id) ? '▾' : '▸'}
                  </span>
                {:else}
                  <span class="chevron-placeholder"></span>
                {/if}
                <span class="label {row.kind}" title={row.title || ''}>{row.label}</span>
              </div>
            {/each}
          </div>
        </div>
      {/if}
      <div class="cm-host" bind:this={hostEl}></div>
    </div>
  {/if}
</div>
