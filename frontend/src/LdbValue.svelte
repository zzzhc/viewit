<script>
  import hljs from 'highlight.js'
  import { copyText } from './viewers.js'
  import { isDark } from './theme.svelte.js'

  // value:leveldbGet 返回的 ldbValue { key, size, text?, base64?, tooBig? }
  let { value } = $props()

  let JsonEditor = $state(null)
  async function loadJsonEditor() {
    if (JsonEditor) return
    JsonEditor = (await import('svelte-jsoneditor')).JSONEditor
  }

  let mode = $state('tree') // json 时:'tree' | 'code'
  let copied = $state('') // 'text' | 'base64' | 'hex',复制反馈

  // 格式自动判读:服务端已按 UTF-8 合法性切分 text/base64;JSON 是 text 的
  // 子类,这里按能否 JSON.parse 判定。
  let format = $derived.by(() => {
    if (value.tooBig) return 'tooBig'
    if (value.text == null) return 'binary'
    try {
      JSON.parse(value.text)
      return 'json'
    } catch {
      return 'text'
    }
  })

  let parsed = $derived.by(() => (format === 'json' ? JSON.parse(value.text) : null))
  let canTree = $derived(parsed !== null && typeof parsed === 'object')
  let pretty = $derived.by(() => (format === 'json' ? JSON.stringify(parsed, null, 2) : ''))
  let jsonHtml = $derived(hljs.highlight(pretty, { language: 'json', ignoreIllegals: true }).value)

  let bytes = $derived.by(() => (format === 'binary' ? base64ToBytes(value.base64) : null))
  let rowCount = $derived(bytes ? Math.ceil(bytes.length / 16) : 0)

  $effect(() => {
    // 进入 json 树形视图时懒加载 svelte-jsoneditor(约 600KB)。
    if (format === 'json' && canTree && mode === 'tree') loadJsonEditor()
  })

  function base64ToBytes(b64) {
    const bin = atob(b64)
    const out = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
    return out
  }

  // 连续十六进制串(无空格),供二进制"复制 hex"。
  function hexString() {
    const b = bytes
    if (!b) return ''
    let s = ''
    for (let i = 0; i < b.length; i++) s += b[i].toString(16).padStart(2, '0')
    return s
  }

  // hexdump -C 风格单行:偏移(8 位十六进制) + 两组 8 字节 + ASCII 侧栏。
  function renderHexRow(bytes, i) {
    const start = i * 16
    const end = Math.min(start + 16, bytes.length)
    const parts = []
    for (let j = 0; j < 16; j++) {
      parts.push(start + j < end ? bytes[start + j].toString(16).padStart(2, '0') : '  ')
    }
    let ascii = ''
    for (let j = 0; j < 16; j++) {
      if (start + j >= end) {
        ascii += ' '
        continue
      }
      const b = bytes[start + j]
      ascii += b >= 0x20 && b <= 0x7e ? String.fromCharCode(b) : '.'
    }
    return {
      offset: start.toString(16).padStart(8, '0'),
      hex: '  ' + parts.slice(0, 8).join(' ') + '  ' + parts.slice(8).join(' '),
      ascii: '  |' + ascii + '|',
    }
  }

  // 二进制 hex 虚拟滚动:固定行高 20px,只渲染可视窗口(大值不撑爆 DOM)。
  const HEX_ROW = 20
  const HEX_OVERSCAN = 10
  let hexEl = $state(undefined)
  let hstart = $state(0)
  let hend = $state(0)
  function hupdate() {
    const el = hexEl
    if (!el) return
    hstart = Math.floor(el.scrollTop / HEX_ROW)
    hend = Math.min(rowCount, Math.ceil((el.scrollTop + el.clientHeight) / HEX_ROW) + HEX_OVERSCAN)
  }
  let visibleRows = $derived.by(() => {
    if (!bytes) return []
    const rows = []
    for (let i = hstart; i < hend; i++) rows.push(renderHexRow(bytes, i))
    return rows
  })
  $effect(() => {
    const el = hexEl
    if (!el) return
    hupdate()
    const ro = new ResizeObserver(hupdate)
    ro.observe(el)
    return () => ro.disconnect()
  })

  function toggleMode() {
    mode = mode === 'tree' ? 'code' : 'tree'
  }

  async function copy(kind, text) {
    if (await copyText(text)) {
      copied = kind
      setTimeout(() => (copied = ''), 1500)
    }
  }

  let label = $derived(format === 'json' ? 'JSON' : format === 'text' ? '文本' : format === 'binary' ? '二进制' : '过大')
</script>

<div class="ldb-value mt-1">
  <div class="flex flex-none items-center gap-2 border-b border-edge px-3 py-1.5">
    <span class="filetype">{label}</span>
    <span class="text-[11px] text-muted">{value.size} 字节</span>
    <span class="flex-1"></span>
    {#if format === 'json' && canTree}
      <button type="button" class="btn ghost" onclick={toggleMode}>
        {mode === 'tree' ? '代码视图' : '树形视图'}
      </button>
    {/if}
    {#if format === 'json' || format === 'text'}
      <button type="button" class="btn ghost" onclick={() => copy('text', value.text)}>
        {copied === 'text' ? '已复制' : '复制'}
      </button>
    {:else if format === 'binary'}
      <button type="button" class="btn ghost" onclick={() => copy('base64', value.base64)}>
        {copied === 'base64' ? '已复制' : '复制 base64'}
      </button>
      <button type="button" class="btn ghost" onclick={() => copy('hex', hexString())}>
        {copied === 'hex' ? '已复制' : '复制 hex'}
      </button>
    {/if}
  </div>

  {#if format === 'tooBig'}
    <div class="hint px-3 py-2">
      <p>值过大({value.size} 字节,超过 5MB 上限);用 dump &lt;prefix&gt; 导出</p>
    </div>
  {:else if format === 'binary'}
    <div class="ldb-value-body">
      <div
        class="min-h-0 flex-1 overflow-auto font-mono text-[13px] leading-[20px]"
        bind:this={hexEl}
        onscroll={hupdate}
      >
        <div class="relative" style="height: {rowCount * HEX_ROW}px">
          {#each visibleRows as row, gi (hstart + gi)}
            <div class="absolute inset-x-0 whitespace-pre px-3" style="top: {(hstart + gi) * HEX_ROW}px; height: {HEX_ROW}px">
              <span class="text-muted">{row.offset}</span><span>{row.hex}</span><span class="text-muted">{row.ascii}</span>
            </div>
          {/each}
        </div>
      </div>
    </div>
  {:else if format === 'json' && canTree && mode === 'tree'}
    <div class="ldb-value-body">
      <div class="json-tree" class:jse-theme-dark={isDark()}>
        {#if JsonEditor}
          <JsonEditor
            content={{ text: value.text }}
            readOnly={true}
            mode="tree"
            mainMenuBar={false}
            statusBar={false}
          />
        {:else}
          <div class="hint">加载中…</div>
        {/if}
      </div>
    </div>
  {:else if format === 'json'}
    <div class="ldb-value-body">
      <pre class="min-h-0 flex-1 overflow-auto whitespace-pre px-3 py-2 font-mono text-[13px] leading-[1.6]"><code>{@html jsonHtml}</code></pre>
    </div>
  {:else}
    <div class="ldb-value-body">
      <pre class="min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-all px-3 py-2 font-mono text-[13px] leading-[1.6]">{value.text}</pre>
    </div>
  {/if}
</div>
