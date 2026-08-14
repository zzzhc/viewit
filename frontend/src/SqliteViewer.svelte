<script>
  import { onMount } from 'svelte'
  import hljs from 'highlight.js'
  import { sqliteTables, sqliteRows, sqliteQuery, sqliteExportUrl } from './api.js'
  import LdbValue from './LdbValue.svelte'

  // props:path sqlite 数据库文件(相对 root)。
  let { path } = $props()

  // ---- 左面板:表列表 ----
  let tables = $state([]) // [{ name, type, sql, rows }]
  let tableFilter = $state('')
  let tablesErr = $state('')

  // ---- 右面板:数据表格 ----
  const ROW_H = 30 // 行高(虚拟滚动与行内 line-height 共用)
  const COL_W = 150 // 列宽默认值(表头可拖拽调整,列多时横向滚动)
  const MIN_COL_W = 60 // 拖拽列宽下限
  const MAX_COL_W = 800 // 拖拽列宽上限
  const OVERSCAN = 10
  const PAGE = 500 // 每页行数(与服务端默认 limit 一致)
  let mode = $state('table') // 'table' 表浏览 | 'query' SQL 结果
  let activeTable = $state('') // table 模式当前表名
  let activeSql = $state('') // query 模式当前 SQL
  let tableInfo = $state(null) // 选中表元信息 { name, type, sql, rows }
  let columns = $state([])
  let rows = $state([]) // [[cell,...],...] 顺序追加;数据源切换时整体重置
  let hasMore = $state(false)
  let loading = $state(false)
  let total = $state(null) // table 模式首屏 COUNT(深分页后服务端不再下发)
  let truncated = $state(false) // query 模式结果超限提示
  let dataErr = $state('')
  let showSchema = $state(false)

  let scroller = $state(undefined)
  let headerEl = $state(undefined)
  let lstart = $state(0)
  let lend = $state(0)
  let visibleRows = $derived.by(() => rows.slice(lstart, lend))

  // ---- 列宽(表头拖拽调整,数据源切换时重置) ----
  let colWidths = $state([]) // 每列宽度(px)
  let colTotal = $derived(colWidths.reduce((a, b) => a + b, 0))
  let resizeCol = $state(-1)
  let resizeStartX = 0
  let resizeStartW = 0

  function onResizeStart(ci, e) {
    e.preventDefault()
    resizeCol = ci
    resizeStartX = e.clientX
    resizeStartW = colWidths[ci] || COL_W
    document.body.style.userSelect = 'none' // 拖拽中禁用文本选择
    window.addEventListener('pointermove', onResizeMove)
    window.addEventListener('pointerup', onResizeEnd)
  }
  function onResizeMove(e) {
    if (resizeCol < 0) return
    const w = Math.min(MAX_COL_W, Math.max(MIN_COL_W, resizeStartW + e.clientX - resizeStartX))
    colWidths = colWidths.map((c, i) => (i === resizeCol ? w : c))
  }
  function onResizeEnd() {
    resizeCol = -1
    document.body.style.userSelect = ''
    window.removeEventListener('pointermove', onResizeMove)
    window.removeEventListener('pointerup', onResizeEnd)
  }

  // ---- 导出(CSV/JSONL,流式下载带进度) ----
  let exportActive = $state(false)
  let exportProgress = $state('')
  let exportReader = null
  let exportCancelled = false

  async function exportData(format) {
    if (exportActive) return
    exportActive = true
    exportCancelled = false
    exportProgress = '0 行'
    let res
    try {
      res = await fetch(
        sqliteExportUrl(path, {
          table: mode === 'table' ? activeTable : '',
          sql: mode === 'query' ? activeSql : '',
          format,
        })
      )
    } catch {
      exportActive = false
      exportProgress = '导出失败:网络错误'
      return
    }
    if (!res.ok) {
      let msg = `HTTP ${res.status}`
      try {
        const data = await res.json()
        if (data && data.error) msg = data.error
      } catch { /* 非 JSON 错误体 */ }
      exportActive = false
      exportProgress = ''
      dataErr = msg
      return
    }
    const reader = res.body.getReader()
    exportReader = reader
    const chunks = []
    let count = 0
    let bytes = 0
    let lastUi = 0
    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        chunks.push(value)
        bytes += value.length
        for (let i = 0; i < value.length; i++) if (value[i] === 10) count++
        const now = performance.now()
        if (now - lastUi > 100) {
          lastUi = now
          exportProgress = `${count} 行 / ${bytes} 字节`
        }
      }
    } catch {
      if (exportCancelled) {
        exportProgress = '已取消'
        exportActive = false
        exportReader = null
        return
      }
      exportProgress = '导出中断'
      exportActive = false
      exportReader = null
      return
    }
    exportReader = null
    exportActive = false
    exportProgress = ''
    const base = (mode === 'query' ? 'query' : activeTable || 'export').replace(/[^A-Za-z0-9._-]+/g, '_')
    const blob = new Blob(chunks, { type: format === 'jsonl' ? 'application/x-ndjson' : 'text/csv' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${base}.${format === 'jsonl' ? 'jsonl' : 'csv'}`
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  function cancelExport() {
    if (!exportActive) return
    exportCancelled = true
    exportReader?.cancel()
  }

  // ---- 单元格值卡片(modal) ----
  let cellModal = $state(null) // { key, size, text?/base64? } 或 { key, note }

  // ---- 命令栏 ----
  let cmdInput = $state('')
  let hist = $state([]) // 命令历史(最新在后)
  let histIdx = $state(0) // == hist.length 表示位于新输入区
  let histBuf = $state('')

  const filteredTables = $derived.by(() => {
    const q = tableFilter.trim().toLowerCase()
    if (!q) return tables
    return tables.filter((t) => t.name.toLowerCase().includes(q))
  })

  // 结构视图 SQL 语法高亮(完整 bundle 已含 sql grammar,主题随深浅色切换)
  let schemaHtml = $derived(
    tableInfo?.sql ? hljs.highlight(tableInfo.sql, { language: 'sql', ignoreIllegals: true }).value : ''
  )

  function fmtRows(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B'
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
    return String(n)
  }

  function fmtBytes(n) {
    if (n >= 1e9) return (n / 1e9).toFixed(1) + ' GB'
    if (n >= 1e6) return (n / 1e6).toFixed(1) + ' MB'
    if (n >= 1e3) return (n / 1e3).toFixed(1) + ' KB'
    return n + ' B'
  }

  async function loadTables() {
    tablesErr = ''
    try {
      const data = await sqliteTables(path)
      tables = data.tables || []
    } catch (e) {
      tablesErr = e.message
    }
  }

  // ---- 虚拟滚动窗口 ----
  function lupdate() {
    const el = scroller
    if (!el) return
    const st = el.scrollTop
    const ch = el.clientHeight
    lstart = Math.floor(st / ROW_H)
    lend = Math.min(rows.length, Math.ceil((st + ch) / ROW_H) + OVERSCAN)
  }
  function onScroll() {
    // 表头横向跟随数据区(表头自身 overflow hidden,滚动条在数据区底部)
    if (headerEl && scroller) headerEl.scrollLeft = scroller.scrollLeft
    lupdate()
  }

  // 数据源切换(开表/新查询)时整体重置
  function resetData() {
    columns = []
    rows = []
    colWidths = []
    hasMore = false
    total = null
    truncated = false
    dataErr = ''
    showSchema = false
    lstart = 0
    lend = 0
    scroller?.scrollTo({ top: 0 })
  }

  async function openTable(name) {
    activeTable = name
    activeSql = ''
    mode = 'table'
    tableInfo = tables.find((t) => t.name === name) || null
    resetData()
    loading = true
    try {
      const data = await sqliteRows(path, name, { offset: 0, limit: PAGE })
      columns = data.columns
      colWidths = columns.map(() => COL_W)
      rows = data.rows
      hasMore = data.hasMore
      total = data.total != null ? data.total : null
    } catch (e) {
      dataErr = e.message
    } finally {
      loading = false
    }
  }

  async function loadMore() {
    if (loading || mode !== 'table') return
    loading = true
    const myMode = mode
    const myTable = activeTable
    try {
      const data = await sqliteRows(path, myTable, { offset: rows.length, limit: PAGE })
      if (myMode !== mode || myTable !== activeTable) return // 已切换:丢弃过期结果
      rows = [...rows, ...data.rows]
      hasMore = data.hasMore
    } catch (e) {
      dataErr = e.message
    } finally {
      loading = false
    }
  }

  async function runQuery(sql) {
    const t = sql.trim()
    if (!t) return
    activeSql = t
    activeTable = ''
    mode = 'query'
    tableInfo = null
    resetData()
    loading = true
    try {
      const data = await sqliteQuery(path, t, { limit: PAGE })
      columns = data.columns
      colWidths = columns.map(() => COL_W)
      rows = data.rows
      truncated = !!data.truncated
    } catch (e) {
      dataErr = e.message
    } finally {
      loading = false
    }
  }

  function runCommand(line) {
    const t = line.trim()
    if (!t) return
    if (t.toLowerCase().startsWith('rows ')) {
      const name = t.slice(5).trim()
      if (name) openTable(name)
      else dataErr = '用法: rows <表名>'
      return
    }
    runQuery(t)
  }

  function onKeydown(e) {
    if (e.key === 'Enter') {
      const line = cmdInput
      if (!line.trim()) return
      hist = [...hist.slice(-99), line]
      histIdx = hist.length
      cmdInput = ''
      runCommand(line)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (histIdx > 0) {
        if (histIdx === hist.length) histBuf = cmdInput
        histIdx--
        cmdInput = hist[histIdx]
      }
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (histIdx < hist.length) {
        histIdx++
        cmdInput = histIdx === hist.length ? histBuf : hist[histIdx]
      }
    }
  }

  // ---- 单元格 ----
  function cellText(cell) {
    if (cell === null) return 'NULL'
    if (typeof cell === 'object') return `BLOB ${fmtBytes(cell.n)}`
    return String(cell)
  }
  function cellTitle(cell) {
    if (typeof cell === 'string' && cell.length > 100) return cell
    return ''
  }
  function openCell(col, cell) {
    if (cell === null) return
    if (typeof cell === 'object') {
      if (cell.big) {
        cellModal = {
          key: col,
          note: `BLOB 过大(${cell.n} 字节),已省略数据;请用 SQL 查询或下载文件查看`,
        }
        return
      }
      cellModal = { key: col, size: cell.n, base64: cell.b }
      return
    }
    const text = String(cell)
    cellModal = { key: col, size: new TextEncoder().encode(text).length, text }
  }
  function onModalKeydown(e) {
    if (e.key === 'Escape') cellModal = null
  }

  onMount(() => {
    window.addEventListener('keydown', onModalKeydown)
    return () => window.removeEventListener('keydown', onModalKeydown)
  })

  // 挂载即加载表列表
  $effect(() => {
    loadTables()
  })

  // 滚动窗口:滚动/尺寸变化(含 rows 替换)时重算
  $effect(() => {
    const el = scroller
    if (!el) return
    lupdate()
    const ro = new ResizeObserver(lupdate)
    ro.observe(el)
    return () => ro.disconnect()
  })

  // 触底哨兵:表浏览模式接近列表尾部且还有更多时,追加一页。
  $effect(() => {
    const n = rows.length
    const e = lend
    const hm = hasMore
    const lm = loading
    if (mode === 'table' && e >= n - 5 && hm && !lm) loadMore()
  })
</script>

<div class="flex h-full min-h-0">
  <!-- 左:表列表 -->
  <div class="flex w-64 flex-none flex-col border-r border-edge">
    <div class="flex-none border-b border-edge bg-panel px-3 py-2">
      <input
        class="w-full border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
        placeholder="过滤表名"
        bind:value={tableFilter}
      />
      <div class="mt-1 text-[11px] text-muted">{tables.length} 张表</div>
    </div>
    <div class="min-h-0 flex-1 overflow-auto bg-bg">
      {#if tablesErr}
        <div class="hint error">
          <p>{tablesErr}</p>
          <button type="button" class="btn" onclick={loadTables}>重试</button>
        </div>
      {:else if filteredTables.length === 0}
        <div class="hint">{tables.length === 0 ? '无表或视图' : '无匹配表'}</div>
      {:else}
        {#each filteredTables as t}
          <button
            type="button"
            class="flex w-full cursor-pointer items-center gap-2 border-0 bg-transparent px-3 py-1.5 text-left hover:bg-hover {t.name === activeTable ? 'bg-hover' : ''}"
            onclick={() => openTable(t.name)}
          >
            <span class="min-w-0 flex-1 truncate font-mono text-[12px] text-fg" title={t.name}>{t.name}</span>
            {#if t.type === 'view'}
              <span class="flex-none rounded border border-edge px-1 text-[10px] text-muted">视图</span>
            {/if}
            <span class="flex-none text-[11px] text-muted">{fmtRows(t.rows)}</span>
          </button>
        {/each}
      {/if}
    </div>
  </div>

  <!-- 右:信息条 + 表格 + 命令栏 -->
  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div class="flex flex-none items-center gap-2 border-b border-edge bg-panel px-3 py-2">
      <span class="min-w-0 truncate font-mono text-[13px] font-semibold" title={mode === 'query' ? activeSql : activeTable}>
        {mode === 'query' ? (activeSql || 'SQL 查询') : (activeTable || 'SQLite')}
      </span>
      {#if total != null}
        <span class="flex-none text-[11px] text-muted">共 {total.toLocaleString()} 行</span>
      {/if}
      {#if truncated}
        <span class="flex-none rounded bg-hover px-1.5 py-0.5 text-[11px] text-muted">结果已截断(&gt;{PAGE} 行),请加 LIMIT</span>
      {/if}
      {#if loading}
        <span class="flex-none text-[11px] text-muted">加载中…</span>
      {/if}
      <span class="flex-1"></span>
      {#if exportActive}
        <span class="flex-none text-[11px] text-muted">导出中… {exportProgress}</span>
        <button type="button" class="btn ghost" onclick={cancelExport}>取消</button>
      {:else if activeTable || activeSql}
        <button type="button" class="btn ghost" onclick={() => exportData('csv')} title="导出为 CSV">CSV</button>
        <button type="button" class="btn ghost" onclick={() => exportData('jsonl')} title="导出为 JSONL">JSONL</button>
      {/if}
      {#if tableInfo?.sql}
        <button type="button" class="btn ghost" onclick={() => (showSchema = !showSchema)}>
          {showSchema ? '隐藏结构' : '结构'}
        </button>
      {/if}
    </div>

    {#if showSchema && tableInfo?.sql}
      <pre class="max-h-48 flex-none overflow-auto whitespace-pre border-b border-edge bg-bg px-3 py-2 font-mono text-[12px] leading-[1.5]"><code>{@html schemaHtml}</code></pre>
    {/if}

    {#if columns.length > 0}
      <!-- 表头:独立横向滚动,随数据区同步;列宽可拖拽调整 -->
      <div class="flex-none overflow-x-hidden border-b border-edge bg-panel" bind:this={headerEl}>
        <div class="flex" style="width: {Math.max(colTotal, 1)}px">
          {#each columns as col, ci}
            <div class="relative flex-none overflow-hidden text-ellipsis whitespace-nowrap px-2 py-1.5 font-mono text-[12px] font-semibold text-muted" style="width: {colWidths[ci] || COL_W}px" title={col}>
              {col}
              <div
                class="absolute right-0 top-0 z-10 h-full w-1.5 cursor-col-resize hover:bg-accent/50"
                role="separator"
                aria-orientation="vertical"
                aria-label="调整列宽"
                onpointerdown={(e) => onResizeStart(ci, e)}
                title="拖拽调整列宽"
              ></div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <!-- 数据表格:固定行高虚拟滚动 -->
    <div class="min-h-0 flex-1 overflow-auto bg-bg" bind:this={scroller} onscroll={onScroll}>
      {#if dataErr}
        <div class="hint error">
          <p>{dataErr}</p>
        </div>
      {:else if rows.length === 0 && !loading}
        <div class="hint">
          {#if !columns.length}
            选择左侧表或输入 SQL 查询<br />
            <span class="text-muted">SELECT … | rows &lt;表名&gt;</span>
          {:else}
            {mode === 'query' ? '查询无结果' : '空表,无数据'}
          {/if}
        </div>
      {:else if rows.length > 0}
        <div class="relative" style="height: {rows.length * ROW_H}px; width: {Math.max(colTotal, 1)}px">
          {#each visibleRows as row, gi (lstart + gi)}
            <div class="absolute inset-x-0 flex border-b border-edge" style="top: {(lstart + gi) * ROW_H}px; height: {ROW_H}px">
              {#each row as cell, ci}
                <div
                  class="flex-none cursor-pointer overflow-hidden text-ellipsis whitespace-nowrap px-2 font-mono text-[12px] leading-[30px] hover:bg-hover {cell === null ? 'italic text-muted' : 'text-fg'}"
                  style="width: {colWidths[ci] || COL_W}px"
                  title={cellTitle(cell)}
                  role="gridcell"
                  tabindex="0"
                  onclick={() => openCell(columns[ci], cell)}
                  onkeydown={(e) => e.key === 'Enter' && openCell(columns[ci], cell)}
                >{cellText(cell)}</div>
              {/each}
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- 命令栏 -->
    <div class="flex-none border-t border-edge bg-panel px-3 py-2">
      <div class="flex items-center gap-2">
        <span class="font-mono text-[13px] text-accent">&gt;</span>
        <input
          class="min-w-0 flex-1 border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
          placeholder="SELECT … | rows &lt;表名&gt;"
          bind:value={cmdInput}
          onkeydown={onKeydown}
        />
      </div>
    </div>
  </div>
</div>

{#if cellModal}
  <div class="fixed inset-0 z-[100] flex items-center justify-center bg-[var(--vt-overlay)]" role="presentation" onclick={() => (cellModal = null)}>
    <div
      class="flex max-h-[80vh] w-[min(760px,92vw)] flex-col overflow-hidden rounded-lg border border-edge bg-panel shadow-[0_16px_48px_rgba(0,0,0,0.5)]"
      onclick={(e) => e.stopPropagation()}
      role="dialog"
      aria-modal="true"
    >
      <div class="flex flex-none items-center gap-2 border-b border-edge px-4 py-2">
        <span class="min-w-0 truncate font-mono text-[13px] font-semibold">{cellModal.key}</span>
        <span class="flex-1"></span>
        <button
          type="button"
          class="cursor-pointer border-0 bg-transparent p-0.5 text-lg leading-none text-muted hover:text-fg"
          title="关闭 (Esc)"
          onclick={() => (cellModal = null)}
        >×</button>
      </div>
      <div class="min-h-0 flex-1 overflow-auto bg-bg p-3">
        {#if cellModal.note}
          <div class="hint">{cellModal.note}</div>
        {:else}
          <LdbValue value={cellModal} />
        {/if}
      </div>
    </div>
  </div>
{/if}
