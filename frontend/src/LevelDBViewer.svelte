<script>
  import { tick } from 'svelte'
  import { leveldbKeys, leveldbGet, leveldbDumpUrl, ldbName } from './api.js'

  // props:path leveldb 数据目录(相对 root)。
  let { path } = $props()

  // ---- 左面板:key 列表(顺序数组,触底 lazy 加载) ----
  const ROW_H = 30
  const OVERSCAN = 10
  let prefix = $state('')
  let keys = $state([]) // 顺序追加;prefix 变化时整体重置
  let hasMore = $state(true)
  let loadingMore = $state(false)
  let activeKey = $state('')
  let seq = 0 // prefix 变化时递增,作废在途请求
  let prefixTimer = 0
  let firstPrefix = true

  let lscroller = $state(undefined)
  let lstart = $state(0)
  let lend = $state(0)
  let visibleKeys = $derived.by(() => keys.slice(lstart, lend))

  function lupdateWindow() {
    const el = lscroller
    if (!el) return
    const st = el.scrollTop
    const ch = el.clientHeight
    lstart = Math.floor(st / ROW_H)
    lend = Math.min(keys.length, Math.ceil((st + ch) / ROW_H) + OVERSCAN)
  }
  const lonscroll = lupdateWindow

  async function loadMore() {
    if (loadingMore) return
    loadingMore = true
    const mySeq = seq
    try {
      const data = await leveldbKeys(path, { prefix, after: keys.at(-1) ?? '', limit: 500 })
      if (mySeq !== seq) return // prefix 已切换:丢弃过期结果
      keys = [...keys, ...data.keys]
      hasMore = data.hasMore
    } catch (e) {
      if (mySeq === seq) {
        appendBlock('key 列表加载失败')
        appendLine({ kind: 'error', text: e.message })
      }
    } finally {
      if (mySeq === seq) loadingMore = false
    }
  }

  // 前缀防抖重置:effect 体内先同步读 prefix(Svelte 5 陷阱:异步回调内
  // 的读取不触发重跑),200ms 后清空列表重新加载。
  $effect(() => {
    const p = prefix
    clearTimeout(prefixTimer)
    if (firstPrefix) {
      firstPrefix = false // 挂载时由触底哨兵做初始加载,避免双请求
      return
    }
    prefixTimer = setTimeout(() => {
      seq++
      keys = []
      hasMore = true
      activeKey = ''
      lscroller?.scrollTo({ top: 0 })
      lupdateWindow()
      loadMore()
    }, 200)
    return () => clearTimeout(prefixTimer)
  })

  $effect(() => {
    const el = lscroller
    if (!el) return
    lupdateWindow()
    const ro = new ResizeObserver(lupdateWindow)
    ro.observe(el)
    return () => ro.disconnect()
  })

  // 触底哨兵:可视窗口接近列表尾部且还有更多时,追加一页。
  $effect(() => {
    const n = keys.length
    const e = lend
    const hm = hasMore
    const lm = loadingMore
    if (e >= n - 5 && hm && !lm) loadMore()
  })

  // ---- 右面板:结果区 + 命令栏 ----
  let results = $state([]) // [{ title, lines:[{kind,text}] }]
  let resultsVersion = $state(0)
  let resultsEl = $state(undefined)

  let cmdInput = $state('')
  let hist = $state([]) // 命令历史(最新在后)
  let histIdx = $state(0) // == hist.length 表示位于新输入区
  let histBuf = $state('')
  let dumpActive = $state(false)
  let dumpProgress = $state('')
  let dumpReader = null
  let dumpCancelled = false

  function appendBlock(title) {
    results = [...results, { title, lines: [] }]
    resultsVersion++
  }
  function appendLine(line) {
    const b = results[results.length - 1]
    if (!b) return null
    b.lines = [...b.lines, line]
    resultsVersion++
    return b.lines[b.lines.length - 1] // 深代理引用:后续改属性可触发响应
  }

  // 最新块出现(或行追加)后滚动到底跟随。
  $effect(() => {
    if (!resultsVersion || !resultsEl) return
    tick().then(() => {
      if (resultsEl) resultsEl.scrollTop = resultsEl.scrollHeight
    })
  })

  function doGet(key) {
    activeKey = key
    runGet(key)
  }

  async function runGet(key) {
    appendBlock(`get ${key}`)
    try {
      const v = await leveldbGet(path, key)
      if (v.tooBig) {
        appendLine({ kind: 'error', text: `值过大(${v.size} 字节,超过 5MB 上限);用 dump <prefix> 导出` })
      } else if (v.text != null) {
        appendLine({ kind: 'value', text: v.text })
      } else {
        appendLine({ kind: 'text', text: `[二进制 ${v.size} 字节, base64]` })
        appendLine({ kind: 'value', text: v.base64 })
      }
    } catch (e) {
      appendLine({ kind: 'error', text: e.message })
    }
  }

  function runSeek(p) {
    appendBlock(`> seek ${p}`)
    appendLine({ kind: 'text', text: p ? `前缀已切换为 "${p}"` : '前缀已清空(全部 key)' })
    prefix = p
  }

  async function runDump(p) {
    if (dumpActive) {
      appendBlock(`> dump ${p}`)
      appendLine({ kind: 'error', text: '已有导出进行中,请先取消' })
      return
    }
    appendBlock(`> dump ${p}`)
    const progressLine = appendLine({ kind: 'progress', text: '导出中… 0 条' })
    dumpActive = true
    dumpCancelled = false
    dumpProgress = '0 条'
    let res
    try {
      res = await fetch(leveldbDumpUrl(path, p))
    } catch {
      dumpActive = false
      progressLine.text = '导出失败:网络错误'
      dumpProgress = ''
      return
    }
    if (!res.ok) {
      let msg = `HTTP ${res.status}`
      try {
        const data = await res.json()
        if (data && data.error) msg = data.error
      } catch { /* 非 JSON 错误体 */ }
      dumpActive = false
      dumpProgress = ''
      progressLine.kind = 'error'
      progressLine.text = msg
      return
    }
    const reader = res.body.getReader()
    dumpReader = reader
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
          progressLine.text = `导出中… ${count} 条 / ${bytes} 字节`
          dumpProgress = `${count} 条`
        }
      }
    } catch {
      if (dumpCancelled) {
        progressLine.text = '已取消'
        dumpProgress = ''
        dumpActive = false
        dumpReader = null
        return
      }
      progressLine.kind = 'error'
      progressLine.text = '导出中断'
      dumpProgress = ''
      dumpActive = false
      dumpReader = null
      return
    }
    dumpReader = null
    dumpActive = false
    dumpProgress = ''
    const name = ldbName(p)
    const blob = new Blob(chunks, { type: 'application/x-ndjson' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
    progressLine.text = `完成: ${count} 条 (${bytes} 字节), 已下载 ${name}`
  }

  function cancelDump() {
    if (!dumpActive) return
    dumpCancelled = true
    dumpReader?.cancel()
  }

  function runCommand(line) {
    const t = line.trim()
    if (!t) return
    const sp = t.indexOf(' ')
    const cmd = sp < 0 ? t : t.slice(0, sp)
    // key 可含空格:rest 原样保留,只 trim 一次前导空格
    const rest = sp < 0 ? '' : t.slice(sp + 1).trim()
    switch (cmd) {
      case 'get':
        if (!rest) {
          appendBlock('> get')
          appendLine({ kind: 'error', text: '用法: get <key>' })
          return
        }
        runGet(rest)
        break
      case 'seek':
        runSeek(rest)
        break
      case 'dump':
        runDump(rest)
        break
      default:
        appendBlock(`> ${t}`)
        appendLine({ kind: 'error', text: '用法: get <key> | seek <prefix> | dump <prefix>' })
    }
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
</script>

<div class="flex h-full min-h-0">
  <!-- 左:key 列表 -->
  <div class="flex w-1/3 min-w-[240px] flex-none flex-col border-r border-edge">
    <div class="flex-none border-b border-edge bg-panel px-3 py-2">
      <input
        class="w-full border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
        placeholder="前缀过滤(空=全部)"
        bind:value={prefix}
      />
      <div class="mt-1 text-[11px] text-muted">{keys.length}{hasMore ? '+' : ''} 个 key</div>
    </div>
    <div class="min-h-0 flex-1 overflow-auto" bind:this={lscroller} onscroll={lonscroll}>
      {#if keys.length === 0 && !loadingMore && !hasMore}
        <div class="hint">无匹配 key</div>
      {:else}
        <div class="relative" style="height: {keys.length * ROW_H}px">
          {#each visibleKeys as key, gi (lstart + gi)}
            <div
              class="absolute inset-x-0 flex cursor-pointer items-center truncate border-b border-edge px-3 font-mono text-[12px] {key === activeKey ? 'bg-hover' : ''} hover:bg-hover"
              style="top: {(lstart + gi) * ROW_H}px; height: {ROW_H}px"
              role="row"
              title={key}
              onclick={() => doGet(key)}
            >{key}</div>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <!-- 右:上结果区 / 下命令栏 -->
  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-auto bg-bg" bind:this={resultsEl}>
      {#if results.length === 0}
        <div class="hint">点击左侧 key 或在下方向导执行命令<br />get &lt;key&gt; | seek &lt;prefix&gt; | dump &lt;prefix&gt;</div>
      {:else}
        <div class="px-4 py-3 font-mono text-[13px] leading-[1.6]">
          {#each results as block}
            <div class="mb-3">
              <div class="mb-0.5 text-[12px] font-semibold text-muted">{block.title}</div>
              {#each block.lines as line}
                {#if line.kind === 'value'}
                  <pre class="whitespace-pre-wrap break-all">{line.text}</pre>
                {:else if line.kind === 'error'}
                  <div class="text-danger">{line.text}</div>
                {:else if line.kind === 'progress'}
                  <div class="text-muted">{line.text}</div>
                {:else}
                  <div>{line.text}</div>
                {/if}
              {/each}
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="flex-none border-t border-edge bg-panel px-3 py-2">
      <div class="flex items-center gap-2">
        <span class="font-mono text-[13px] text-accent">&gt;</span>
        <input
          class="min-w-0 flex-1 border-0 bg-transparent font-mono text-[13px] outline-none placeholder:text-muted"
          placeholder="get &lt;key&gt; | seek &lt;prefix&gt; | dump &lt;prefix&gt;"
          bind:value={cmdInput}
          onkeydown={onKeydown}
        />
        {#if dumpActive}
          <button type="button" class="btn ghost" onclick={cancelDump}>取消</button>
        {/if}
      </div>
      {#if dumpActive}
        <div class="mt-1 text-[12px] text-muted">导出中… {dumpProgress},点击取消停止</div>
      {/if}
    </div>
  </div>
</div>
