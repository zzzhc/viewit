<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { copyText } from './viewers.js'
  import { isDark } from './theme.svelte.js'

  const MAX_SIZE = 5 * 1024 * 1024
  // 表格直接渲染整张 DOM,无虚拟滚动:行数设上限,超限提示而非硬撑
  const MAX_ROWS = 2000
  // 对象键并集上限,防止字段爆炸的异常文件把列宽撑到不可用
  const MAX_COLUMNS = 50
  // 单元格/标量行的显示截断长度
  const MAX_CELL = 200

  let { path, name } = $props()

  let text = $state('')
  let lineNums = $state([])
  let rows = $state([]) // { no, text, ok, value, isObject }
  let columns = $state([]) // 对象键并集（首见顺序）
  let tooBig = $state(false)
  let error = $state('')
  let copied = $state(false)
  let mode = $state('table') // 'table' | 'raw'
  let totalLines = $state(0)
  let objectCount = $state(0)
  let errorCount = $state(0)
  let selectedRow = $state(null) // 当前查看详情的行
  let detailMode = $state('tree') // 'tree' | 'code'
  let detailCopied = $state(false)
  let JsonEditor = $state(null) // svelte-jsoneditor 懒加载模块

  let canTable = $derived(objectCount > 0)
  let visibleRows = $derived(rows.slice(0, MAX_ROWS))
  let rowTruncated = $derived(rows.length > MAX_ROWS)
  // 选中行序列化后的完整 JSON(美化),供详情代码视图与树视图共用
  let detailText = $derived(selectedRow && selectedRow.ok ? JSON.stringify(selectedRow.value, null, 2) : '')
  let detailHtml = $derived(detailText ? hljs.highlight(detailText, { language: 'json', ignoreIllegals: true }).value : '')
  let detailCanTree = $derived(!!selectedRow && selectedRow.ok && selectedRow.value !== null && typeof selectedRow.value === 'object')

  async function loadJsonEditor() {
    if (JsonEditor) return
    JsonEditor = (await import('svelte-jsoneditor')).JSONEditor
  }

  $effect(() => {
    load()
  })

  async function load() {
    text = ''
    lineNums = []
    rows = []
    columns = []
    tooBig = false
    error = ''
    copied = false
    mode = 'table'
    totalLines = 0
    objectCount = 0
    errorCount = 0
    selectedRow = null
    detailMode = 'tree'
    detailCopied = false
    try {
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
      const raw = text.split('\n')
      if (raw[raw.length - 1] === '') raw.pop() // 末尾换行不是一行
      totalLines = raw.length
      lineNums = Array.from({ length: raw.length }, (_, i) => i + 1)

      const nextRows = []
      const keySet = new Set()
      let obj = 0
      let err = 0
      for (let i = 0; i < raw.length; i++) {
        const line = raw[i]
        if (!line.trim()) continue // 空行：既非对象也非错误，跳过
        try {
          const value = JSON.parse(line)
          const isObject = value !== null && typeof value === 'object' && !Array.isArray(value)
          if (isObject) {
            obj++
            for (const k of Object.keys(value)) {
              if (keySet.size < MAX_COLUMNS) keySet.add(k)
            }
          }
          nextRows.push({ no: i + 1, text: line, ok: true, value, isObject })
        } catch {
          err++
          nextRows.push({ no: i + 1, text: line, ok: false })
        }
      }
      rows = nextRows
      columns = [...keySet]
      objectCount = obj
      errorCount = err
      mode = obj > 0 ? 'table' : 'raw'
    } catch (e) {
      error = e.message
    }
  }

  function cellText(v) {
    if (v === undefined) return ''
    if (v === null) return 'null'
    let s
    if (typeof v === 'string') s = v
    else if (typeof v === 'number' || typeof v === 'boolean') return String(v)
    else s = JSON.stringify(v)
    return s.length > MAX_CELL ? s.slice(0, MAX_CELL) + '…' : s
  }

  function scalarText(v) {
    const s = JSON.stringify(v)
    return s.length > MAX_CELL ? s.slice(0, MAX_CELL) + '…' : s
  }

  function errText(line) {
    return line.length > MAX_CELL ? line.slice(0, MAX_CELL) + '…' : line
  }

  function typeLabel(v) {
    if (v === null) return 'null'
    if (Array.isArray(v)) return '数组'
    if (typeof v === 'object') return '对象'
    if (typeof v === 'string') return '字符串'
    if (typeof v === 'number') return '数字'
    if (typeof v === 'boolean') return '布尔'
    return '值'
  }

  function selectRow(row) {
    if (!row || !row.ok) return
    selectedRow = row
    detailMode = 'tree'
    if (row.value !== null && typeof row.value === 'object') loadJsonEditor()
  }

  function closeDetail() {
    selectedRow = null
  }

  function toggleDetailMode() {
    if (detailMode === 'tree') {
      detailMode = 'code'
    } else {
      detailMode = 'tree'
      loadJsonEditor()
    }
  }

  async function copy() {
    if (await copyText(text)) {
      copied = true
      setTimeout(() => (copied = false), 1500)
    }
  }

  async function copyDetail() {
    if (await copyText(detailText)) {
      detailCopied = true
      setTimeout(() => (detailCopied = false), 1500)
    }
  }
</script>

<div class="viewer flex flex-col">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      <span class="filetype">JSONL</span>
    </span>
    {#if !tooBig && !error}
      <span class="toolbar-actions">
        {#if canTable}
          <button class="btn ghost" onclick={() => (mode = mode === 'table' ? 'raw' : 'table')}>
            {mode === 'table' ? '原始' : '表格'}
          </button>
        {/if}
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
      <span>共 {totalLines} 行 · {objectCount} 条对象</span>
      {#if errorCount}<span class="text-danger">{errorCount} 条解析失败</span>{/if}
      {#if rowTruncated}<span>表格仅显示前 {MAX_ROWS} 行</span>{/if}
    </div>

    {#if mode === 'table' && canTable}
      <div class="flex min-h-0 flex-1">
        <div class="jsonl-table-wrap">
          <table class="jsonl-table">
            <thead>
              <tr>
                <th class="col-no">#</th>
                {#each columns as col}<th>{col}</th>{/each}
              </tr>
            </thead>
            <tbody>
              {#each visibleRows as row}
                {#if row.ok && row.isObject}
                  <tr class="clickable" class:sel-row={selectedRow === row} onclick={() => selectRow(row)}>
                    <td class="col-no">{row.no}</td>
                    {#each columns as col}<td>{cellText(row.value[col])}</td>{/each}
                  </tr>
                {:else if row.ok}
                  <tr class="row-scalar clickable" class:sel-row={selectedRow === row} onclick={() => selectRow(row)}>
                    <td class="col-no">{row.no}</td>
                    <td colspan={Math.max(1, columns.length)} class="row-full">{scalarText(row.value)}</td>
                  </tr>
                {:else}
                  <tr class="row-error">
                    <td class="col-no">{row.no}</td>
                    <td colspan={Math.max(1, columns.length)} class="row-full">解析失败：{errText(row.text)}</td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>

        {#if selectedRow}
          <div class="jsonl-detail">
            <div class="jsonl-detail-toolbar">
              <span class="flex min-w-0 items-center gap-2">
                <span class="filename">第 {selectedRow.no} 行</span>
                <span class="filetype">{typeLabel(selectedRow.value)}</span>
              </span>
              <span class="toolbar-actions">
                {#if detailCanTree}
                  <button class="btn ghost" onclick={toggleDetailMode}>
                    {detailMode === 'tree' ? '代码视图' : '树形视图'}
                  </button>
                {/if}
                <button class="btn" onclick={copyDetail}>{detailCopied ? '已复制' : '复制'}</button>
                <button class="btn ghost" onclick={closeDetail}>关闭</button>
              </span>
            </div>

            {#if detailCanTree && detailMode === 'tree'}
              <div class="json-tree" class:jse-theme-dark={isDark()}>
                {#if JsonEditor}
                  <JsonEditor content={{ text: detailText }} readOnly={true} mode="tree" mainMenuBar={false} statusBar={false} />
                {:else}
                  <div class="hint">加载中…</div>
                {/if}
              </div>
            {:else}
              <div class="code-wrap">
                <pre><code>{@html detailHtml}</code></pre>
              </div>
            {/if}
          </div>
        {/if}
      </div>
    {:else}
      <div class="code-wrap">
        <div class="gutter">
          {#each lineNums as n}<span>{n}</span>{/each}
        </div>
        <pre><code>{text}</code></pre>
      </div>
    {/if}
  {/if}
</div>
