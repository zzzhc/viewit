<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { formatSize } from './format.js'
  import { StreamReader } from './stream.js'
  import { codeLanguage, languageFromMime, languageLabel, copyText } from './viewers.js'
  import { isDark } from './theme.svelte.js'

  // 每次向服务端拉取的字节数;服务端另有上限,超限会被截断。
  const CHUNK = 256 * 1024
  // 固定行高(px),与 CSS 的 .stream-line 一致,是虚拟滚动的计量单位。
  const LINE_H = 20
  // 可视窗口上下各多渲染的行数,滚动时避免露出未渲染的空白。
  const OVERSCAN = 40
  // 距底部不足该像素时预取下一块,保证向下滚动平滑。
  const PREFETCH = 2400

  let { path, name } = $props()

  let meta = $state(null) // { name, mime } 服务端下发的内层信息
  let lines = $state([]) // 已加载的全部行(字符串数组)
  let eof = $state(false)
  let error = $state('')
  let loadedBytes = $state(0)
  let lang = $state(null) // hljs 语言名;null=纯文本(不高亮)
  let langLabel = $state('')
  let copied = $state(false)
  let winStart = $state(0) // 当前渲染窗口的起止行(下标),撑起上下留白
  let winEnd = $state(0)
  let windowRows = $state([]) // 当前可见窗口 { no, html }
  let selected = $state(null) // { no, value } 选中的行(仅 JSON 行可选中)
  let detailMode = $state('tree') // 'tree' | 'code'
  let detailCopied = $state(false)
  let JsonEditor = $state(null) // svelte-jsoneditor 懒加载模块

  let scrollEl
  let pending = '' // 尚未凑成整行的尾部文本(非响应式)
  let streaming = false // 是否有未决的 more 请求(非响应式)
  let rafPending = false
  let stream = null

  let gutterDigits = $derived(String(lines.length || 1).length)
  let textViewable = $derived(!meta || innerIsText(meta.mime))
  // 选中行序列化后的完整 JSON(美化),供详情代码视图与树视图共用
  let detailText = $derived(selected ? JSON.stringify(selected.value, null, 2) : '')
  let detailHtml = $derived(detailText ? hljs.highlight(detailText, { language: 'json', ignoreIllegals: true }).value : '')
  let detailCanTree = $derived(!!selected && selected.value !== null && typeof selected.value === 'object')
  function innerIsText(mime) {
    const mt = (mime || '').split(';')[0].trim().toLowerCase()
    return (
      mt.startsWith('text/') ||
      mt === 'application/json' ||
      mt === 'application/xml' ||
      mt === 'application/javascript' ||
      mt === 'application/x-javascript' ||
      mt === 'application/ld+json'
    )
  }

  function innerLang(m) {
    return codeLanguage(m.name) || languageFromMime(m.mime)
  }

  function escapeHtml(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  }

  // 把解码后的文本按行增量拆分:保留未完成的尾部,只追加完整行。
  function appendText(text) {
    pending += text
    const idx = pending.lastIndexOf('\n')
    if (idx === -1) return
    const complete = pending.slice(0, idx + 1)
    pending = pending.slice(idx + 1)
    const parts = complete.split('\n')
    if (parts.length && parts[parts.length - 1] === '') parts.pop()
    lines.push(...parts)
  }

  function renderWindow(a, b) {
    const slice = lines.slice(a, b)
    let parts
    if (lang) {
      parts = hljs.highlight(slice.join('\n'), { language: lang, ignoreIllegals: true }).value.split('\n')
    } else {
      parts = slice.map(escapeHtml)
    }
    windowRows = slice.map((_, i) => ({ no: a + i + 1, html: parts[i] ?? '' }))
  }

  function recomputeWindow() {
    if (!scrollEl) return
    const top = scrollEl.scrollTop
    const viewH = scrollEl.clientHeight
    const first = Math.max(0, Math.floor(top / LINE_H) - OVERSCAN)
    const count = Math.ceil(viewH / LINE_H) + OVERSCAN * 2
    const last = Math.min(lines.length, first + count)
    winStart = first
    winEnd = last
    renderWindow(first, last)
  }

  function maybeFetchMore() {
    if (eof || error || streaming || !scrollEl || !stream) return
    const el = scrollEl
    if (el.scrollHeight - el.scrollTop - el.clientHeight < PREFETCH) {
      streaming = true
      stream.request(CHUNK)
    }
  }

  function onScroll() {
    maybeFetchMore()
    if (rafPending) return
    rafPending = true
    requestAnimationFrame(() => {
      rafPending = false
      recomputeWindow()
    })
  }

  function onMeta(m) {
    meta = m
    if (innerIsText(m.mime)) {
      lang = innerLang(m)
      langLabel = lang && lang !== 'plaintext' ? languageLabel(lang) : ''
      streaming = true
      stream.request(CHUNK)
    } else {
      // 内层是二进制:不拉流、不检测语言,交给下载提示分支。
      lang = null
      langLabel = ''
    }
  }

  function onData(text, byteLength) {
    appendText(text)
    loadedBytes += byteLength
    streaming = false
    recomputeWindow()
    maybeFetchMore()
  }

  function onEnd(total) {
    if (pending) {
      lines.push(pending)
      pending = ''
    }
    eof = true
    streaming = false
    if (total) loadedBytes = total
    recomputeWindow()
  }

  function onError(msg) {
    error = msg
    streaming = false
  }

  async function copy() {
    if (await copyText(lines.join('\n'))) {
      copied = true
      setTimeout(() => (copied = false), 1500)
    }
  }
  async function loadJsonEditor() {
    if (JsonEditor) return
    JsonEditor = (await import('svelte-jsoneditor')).JSONEditor
  }

  // 选中一行:仅能 JSON.parse 成功的行可展开详情(与 JsonlViewer 一致)。
  function selectLine(no) {
    const line = lines[no - 1]
    if (line === undefined) return
    let value
    try {
      value = JSON.parse(line)
    } catch {
      return
    }
    selected = { no, value }
    detailMode = 'tree'
    if (value !== null && typeof value === 'object') loadJsonEditor()
  }

  function closeDetail() {
    selected = null
  }

  function toggleDetailMode() {
    if (detailMode === 'tree') {
      detailMode = 'code'
    } else {
      detailMode = 'tree'
      loadJsonEditor()
    }
  }

  async function copyDetail() {
    if (await copyText(detailText)) {
      detailCopied = true
      setTimeout(() => (detailCopied = false), 1500)
    }
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

  $effect(() => {
    const p = path
    // 重建连接前重置全部状态(切换文件时组件实例复用)。
    meta = null
    lines = []
    eof = false
    error = ''
    loadedBytes = 0
    lang = null
    langLabel = ''
    copied = false
    windowRows = []
    selected = null
    detailMode = 'tree'
    detailCopied = false
    pending = ''
    streaming = false
    if (stream) {
      stream.close()
      stream = null
    }
    const s = new StreamReader({ path: p, onMeta, onData, onEnd, onError })
    stream = s
    s.connect()
    return () => {
      if (stream === s) {
        stream.close()
        stream = null
      }
    }
  })
</script>

<div class="viewer flex flex-col">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      {#if langLabel}<span class="filetype">{langLabel}</span>{/if}
    </span>
    {#if !error && textViewable}
      <span class="toolbar-actions">
        <span class="stream-stat">已加载 {formatSize(loadedBytes)} · {lines.length} 行</span>
        <button class="btn ghost" onclick={copy}>{copied ? '已复制' : '复制'}</button>
        <a class="btn ghost" href={fileUrl(path)} download>下载</a>
      </span>
    {/if}
  </div>

  {#if error}
    <div class="hint error">{error}</div>
  {:else if meta && !textViewable}
    <div class="hint">
      <p>此文件内容为二进制({meta.mime}),无法文本预览</p>
      <a class="btn" href={fileUrl(path)} download>下载</a>
    </div>
  {:else}
    <div class="flex min-h-0 flex-1">
      <div class="stream-scroll" bind:this={scrollEl} onscroll={onScroll}>
        {#if lines.length === 0}
          <div class="hint">{eof ? '空文件' : '加载中…'}</div>
        {/if}
        <div class="stream-body">
          <div style="height:{winStart * LINE_H}px"></div>
          {#each windowRows as w (w.no)}
            <div
              class="stream-line"
              class:selected={selected?.no === w.no}
              onclick={() => selectLine(w.no)}
            >
              <span class="stream-gutter" style="width:{gutterDigits + 1}ch">{w.no}</span>
              <code class="stream-code">{@html w.html}</code>
            </div>
          {/each}
          <div style="height:{(lines.length - winEnd) * LINE_H}px"></div>
        </div>
      </div>

      {#if selected}
        <div class="jsonl-detail">
          <div class="jsonl-detail-toolbar">
            <span class="flex min-w-0 items-center gap-2">
              <span class="filename">第 {selected.no} 行</span>
              <span class="filetype">{typeLabel(selected.value)}</span>
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
  {/if}
</div>
