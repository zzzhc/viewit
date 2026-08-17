<script>
  import { onMount } from 'svelte'
  import { parquetMeta, parquetRows, parquetExportUrl } from './api.js'
  import LdbValue from './LdbValue.svelte'

  // props:path parquet 文件(相对 root)。
  let { path } = $props()

  // ---- 左面板:列列表 ----
  let columnsMeta = $state([]) // [{ name, type, repetition }]
  let colFilter = $state('')
  let metaErr = $state('')
  let createdBy = $state('')
  let rowGroups = $state(0)
  let metaRows = $state(null) // footer 行数

  // ---- 右面板:数据表格 ----
  const ROW_H = 30
  const COL_W = 150
  const MIN_COL_W = 60
  const MAX_COL_W = 800
  const OVERSCAN = 10
  const PAGE = 500
  let columns = $state([])
  let rows = $state([])
  let hasMore = $state(false)
  let loading = $state(false)
  let total = $state(null)
  let dataErr = $state('')
  let showSchema = $state(false)
  let loaded = $state(false) // 是否已尝试加载过数据
  let fetchSeq = 0

  // ---- 字段过滤 ----
  const OPS = [
    { id: '~', label: '包含' },
    { id: '=', label: '等于' },
    { id: '!=', label: '不等于' },
    { id: '>', label: '大于' },
    { id: '>=', label: '大于等于' },
    { id: '<', label: '小于' },
    { id: '<=', label: '小于等于' },
    { id: 'null', label: '为空' },
    { id: 'nnull', label: '非空' },
  ]
  let filters = $state([]) // [{ col, op, val }]
  let filterCol = $state('') // '' = 任意列
  let filterOp = $state('~')
  let filterVal = $state('')
  let filtering = $derived(filters.length > 0)
  const needValue = $derived(filterOp !== 'null' && filterOp !== 'nnull')

  function opLabel(id) {
    return OPS.find((o) => o.id === id)?.label || id
  }
  function addFilter() {
    const op = filterOp
    const col = filterCol
    const val = needValue ? filterVal : ''
    if (needValue && !val.trim() && op === '~') return
    const next = { col, op, val }
    if (filters.some((f) => f.col === next.col && f.op === next.op && f.val === next.val)) return
    filters = [...filters, next]
    filterVal = ''
    loadFirst()
  }
  function removeFilter(i) {
    filters = filters.filter((_, idx) => idx !== i)
    loadFirst()
  }
  function clearFilters() {
    if (!filters.length) return
    filters = []
    loadFirst()
  }
  function onFilterKeydown(e) {
    if (e.key === 'Enter') addFilter()
  }
  function pickCol(name) {
    filterCol = name
  }

  let scroller = $state(undefined)
  let headerEl = $state(undefined)
  let lstart = $state(0)
  let lend = $state(0)
  let visibleRows = $derived.by(() => rows.slice(lstart, lend))

  let colWidths = $state([])
  let colTotal = $derived(colWidths.reduce((a, b) => a + b, 0))
  let resizeCol = $state(-1)
  let resizeStartX = 0
  let resizeStartW = 0

  function onResizeStart(ci, e) {
    e.preventDefault()
    resizeCol = ci
    resizeStartX = e.clientX
    resizeStartW = colWidths[ci] || COL_W
    document.body.style.userSelect = 'none'
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
      res = await fetch(parquetExportUrl(path, format, filters))
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
    const base = (path.split('/').pop() || 'export').replace(/\.parquet$/i, '').replace(/[^A-Za-z0-9._-]+/g, '_')
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

  let cellModal = $state(null)

  const filteredCols = $derived.by(() => {
    const q = colFilter.trim().toLowerCase()
    if (!q) return columnsMeta
    return columnsMeta.filter((c) => c.name.toLowerCase().includes(q) || c.type.toLowerCase().includes(q))
  })

  const schemaText = $derived(
    columnsMeta.map((c) => `${c.name}\t${c.type}\t${repLabel(c.repetition)}`).join('\n')
  )

  function repLabel(r) {
    if (r === 'optional') return '可空'
    if (r === 'repeated') return '重复'
    return '必填'
  }

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

  async function loadMeta() {
    metaErr = ''
    try {
      const data = await parquetMeta(path)
      columnsMeta = data.columns || []
      metaRows = data.rows != null ? data.rows : null
      createdBy = data.createdBy || ''
      rowGroups = data.rowGroups || 0
    } catch (e) {
      metaErr = e.message
    }
  }

  function lupdate() {
    const el = scroller
    if (!el) return
    const st = el.scrollTop
    const ch = el.clientHeight
    lstart = Math.floor(st / ROW_H)
    lend = Math.min(rows.length, Math.ceil((st + ch) / ROW_H) + OVERSCAN)
  }
  function onScroll() {
    if (headerEl && scroller) headerEl.scrollLeft = scroller.scrollLeft
    lupdate()
  }

  function resetData() {
    columns = []
    rows = []
    colWidths = []
    hasMore = false
    total = null
    dataErr = ''
    lstart = 0
    lend = 0
    loaded = false
    scroller?.scrollTo({ top: 0 })
  }

  async function loadFirst() {
    const my = ++fetchSeq
    const myFilters = filters
    resetData()
    loading = true
    loaded = true
    try {
      const data = await parquetRows(path, { offset: 0, limit: PAGE, filters: myFilters })
      if (my !== fetchSeq) return
      columns = data.columns || []
      colWidths = columns.map(() => COL_W)
      rows = data.rows || []
      hasMore = data.hasMore
      total = data.total != null ? data.total : myFilters.length ? null : metaRows
    } catch (e) {
      if (my !== fetchSeq) return
      dataErr = e.message
    } finally {
      if (my === fetchSeq) loading = false
    }
  }

  async function loadMore() {
    if (loading) return
    const my = ++fetchSeq
    const myFilters = filters
    loading = true
    const n = rows.length
    try {
      const data = await parquetRows(path, { offset: n, limit: PAGE, filters: myFilters })
      if (my !== fetchSeq || n !== rows.length) return
      rows = [...rows, ...data.rows]
      hasMore = data.hasMore
    } catch (e) {
      if (my !== fetchSeq) return
      dataErr = e.message
    } finally {
      if (my === fetchSeq) loading = false
    }
  }

  function isBlobCell(cell) {
    return cell && typeof cell === 'object' && !Array.isArray(cell) && typeof cell.b === 'string' && typeof cell.n === 'number'
  }

  function cellText(cell) {
    if (cell === null) return 'NULL'
    if (typeof cell === 'object') {
      if (cell.big) return `过大 ${fmtBytes(cell.n)}`
      if (isBlobCell(cell)) return `BLOB ${fmtBytes(cell.n)}`
      try {
        return JSON.stringify(cell)
      } catch {
        return String(cell)
      }
    }
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
          note: `值过大(${cell.n} 字节),已省略数据;请导出 CSV/JSONL 查看`,
        }
        return
      }
      if (isBlobCell(cell)) {
        cellModal = { key: col, size: cell.n, base64: cell.b }
        return
      }
      const text = JSON.stringify(cell, null, 2)
      cellModal = { key: col, size: new TextEncoder().encode(text).length, text }
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

  $effect(() => {
    const p = path
    filters = []
    filterCol = ''
    filterVal = ''
    loadMeta().then(() => {
      if (p === path) loadFirst()
    })
  })

  $effect(() => {
    const el = scroller
    if (!el) return
    lupdate()
    const ro = new ResizeObserver(lupdate)
    ro.observe(el)
    return () => ro.disconnect()
  })

  $effect(() => {
    const n = rows.length
    const e = lend
    const hm = hasMore
    const lm = loading
    if (e >= n - 5 && hm && !lm && n > 0) loadMore()
  })
</script>

<div class="flex h-full min-h-0">
  <div class="flex w-64 flex-none flex-col border-r border-edge">
    <div class="flex-none border-b border-edge bg-panel px-3 py-2">
      <input
        class="w-full border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
        placeholder="过滤列名"
        bind:value={colFilter}
      />
      <div class="mt-1 text-[11px] text-muted">{columnsMeta.length} 列{metaRows != null ? ` · ${fmtRows(metaRows)} 行` : ''}</div>
    </div>
    <div class="min-h-0 flex-1 overflow-auto bg-bg">
      {#if metaErr}
        <div class="hint error">
          <p>{metaErr}</p>
          <button type="button" class="btn" onclick={() => loadMeta().then(() => loadFirst())}>重试</button>
        </div>
      {:else if filteredCols.length === 0}
        <div class="hint">{columnsMeta.length === 0 ? '无列' : '无匹配列'}</div>
      {:else}
        {#each filteredCols as c}
          <button
            type="button"
            class="flex w-full cursor-pointer items-center gap-2 border-0 bg-transparent px-3 py-1.5 text-left hover:bg-hover {c.name === filterCol ? 'bg-hover' : ''}"
            title="按此列过滤"
            onclick={() => pickCol(c.name)}
          >
            <span class="min-w-0 flex-1 truncate font-mono text-[12px] text-fg" title={c.name}>{c.name}</span>
            <span class="flex-none text-[10px] text-muted" title={repLabel(c.repetition)}>{c.type}</span>
          </button>
        {/each}
      {/if}
    </div>
  </div>

  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div class="flex flex-none items-center gap-2 border-b border-edge bg-panel px-3 py-2">
      <span class="min-w-0 truncate font-mono text-[13px] font-semibold">Parquet</span>
      {#if total != null}
        <span class="flex-none text-[11px] text-muted">{filtering ? '匹配' : '共'} {total.toLocaleString()} 行</span>
      {:else if filtering}
        <span class="flex-none text-[11px] text-muted">已加载 {rows.length.toLocaleString()} 行</span>
      {/if}
      {#if columns.length}
        <span class="flex-none text-[11px] text-muted">{columns.length} 列</span>
      {/if}
      {#if rowGroups}
        <span class="flex-none text-[11px] text-muted">{rowGroups} 个 row group</span>
      {/if}
      {#if loading}
        <span class="flex-none text-[11px] text-muted">加载中…</span>
      {/if}
      <span class="flex-1"></span>
      {#if exportActive}
        <span class="flex-none text-[11px] text-muted">导出中… {exportProgress}</span>
        <button type="button" class="btn ghost" onclick={cancelExport}>取消</button>
      {:else}
        <button type="button" class="btn ghost" onclick={() => exportData('csv')} title="导出为 CSV">CSV</button>
        <button type="button" class="btn ghost" onclick={() => exportData('jsonl')} title="导出为 JSONL">JSONL</button>
      {/if}
      {#if columnsMeta.length}
        <button type="button" class="btn ghost" onclick={() => (showSchema = !showSchema)}>
          {showSchema ? '隐藏结构' : '结构'}
        </button>
      {/if}
    </div>

    <div class="flex flex-none flex-col gap-1.5 border-b border-edge bg-panel px-3 py-2">
      <div class="flex items-center gap-2">
        <select
          class="max-w-[40%] rounded border border-edge bg-bg px-1.5 py-1 font-mono text-[12px] text-fg outline-none"
          bind:value={filterCol}
          title="过滤列"
        >
          <option value="">任意列</option>
          {#each columnsMeta as c}
            <option value={c.name}>{c.name}</option>
          {/each}
        </select>
        <select
          class="rounded border border-edge bg-bg px-1.5 py-1 font-mono text-[12px] text-fg outline-none"
          bind:value={filterOp}
        >
          {#each OPS as o}
            <option value={o.id}>{o.label}</option>
          {/each}
        </select>
        {#if needValue}
          <input
            class="min-w-0 flex-1 border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
            placeholder="字段值,回车添加"
            bind:value={filterVal}
            onkeydown={onFilterKeydown}
          />
        {:else}
          <span class="min-w-0 flex-1"></span>
        {/if}
        <button type="button" class="btn ghost" onclick={addFilter}>添加</button>
        {#if filtering}
          <button type="button" class="btn ghost" onclick={clearFilters}>清除</button>
        {/if}
      </div>
      {#if filtering}
        <div class="flex flex-wrap gap-1.5">
          {#each filters as f, i}
            <button
              type="button"
              class="flex items-center gap-1 rounded border border-edge bg-bg px-1.5 py-0.5 font-mono text-[11px] text-fg hover:border-accent"
              title="移除"
              onclick={() => removeFilter(i)}
            >
              <span class="max-w-[12rem] truncate">{f.col || '任意列'} {opLabel(f.op)}{f.op !== 'null' && f.op !== 'nnull' ? ' ' + f.val : ''}</span>
              <span class="text-muted">×</span>
            </button>
          {/each}
        </div>
      {/if}
    </div>

    {#if showSchema}
      <pre class="max-h-48 flex-none overflow-auto whitespace-pre border-b border-edge bg-bg px-3 py-2 font-mono text-[12px] leading-[1.5]">{schemaText}{#if createdBy}

# created by: {createdBy}{/if}</pre>
    {/if}

    {#if columns.length > 0}
      <div class="flex-none overflow-x-hidden border-b border-edge bg-panel" bind:this={headerEl}>
        <div class="flex" style="width: {Math.max(colTotal, 1)}px">
          {#each columns as col, ci}
            <div
              class="relative flex-none cursor-pointer overflow-hidden text-ellipsis whitespace-nowrap px-2 py-1.5 font-mono text-[12px] font-semibold {col === filterCol ? 'text-accent' : 'text-muted'}"
              style="width: {colWidths[ci] || COL_W}px"
              title="按 {col} 过滤"
              role="button"
              tabindex="0"
              onclick={() => pickCol(col)}
              onkeydown={(e) => e.key === 'Enter' && pickCol(col)}
            >
              {col}
              <div
                class="absolute right-0 top-0 z-10 h-full w-1.5 cursor-col-resize hover:bg-accent/50"
                role="separator"
                aria-orientation="vertical"
                aria-label="调整列宽"
                onpointerdown={(e) => onResizeStart(ci, e)}
                onclick={(e) => e.stopPropagation()}
                title="拖拽调整列宽"
              ></div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <div class="min-h-0 flex-1 overflow-auto bg-bg" bind:this={scroller} onscroll={onScroll}>
      {#if dataErr}
        <div class="hint error">
          <p>{dataErr}</p>
        </div>
      {:else if rows.length === 0 && !loading && loaded}
        <div class="hint">{columns.length ? (filtering ? '无匹配行' : '空表,无数据') : '无数据'}</div>
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
