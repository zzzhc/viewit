<script>
  import { formatSize, formatDate } from './format.js'
  import { downloadUrl, listDir, LIST_CHUNK } from './api.js'

  // props:path 当前目录;initial 首屏分页块 {total, entries};onNavigate 点击行导航。
  let { path, initial, onNavigate } = $props()

  // 固定行高虚拟滚动:只渲染可视区行,大目录(数万行)也能即时出结果、
  // 流畅滚动。ROW_H 必须与行 div 的 style height 一致,否则滚动会跳。
  const ROW_H = 34
  const OVERSCAN = 10 // 可视区上下各多渲染的行数,提前填充滚动缓冲

  let scroller = $state(undefined)
  let start = $state(0)
  let end = $state(0)
  // 组件由 {#key path} 按目录重建,挂载时用 initial 初始化分页状态,
  // 目录切换 = 组件销毁重建,无需重置 effect。
  let total = $state(initial?.total || 0)
  let chunks = $state(new Map(initial?.entries?.length ? [[0, initial.entries]] : []))
  let requested = $state(new Set())
  let seq = 0 // 目录切换时递增,作废在途请求
  let fetchTimer = 0

  function entryPath(name) {
    return path === '/' ? name : path + '/' + name
  }

  // 可视行:合并已加载页,未加载的页显示占位(null)
  let visible = $derived.by(() => {
    const out = new Array(end - start)
    for (let i = start; i < end; i++) {
      const ci = Math.floor(i / LIST_CHUNK) * LIST_CHUNK
      const arr = chunks.get(ci)
      out[i - start] = arr ? arr[i - ci] : null
    }
    return out
  })

  async function fetchChunk(offset) {
    requested = new Set(requested).add(offset)
    const mySeq = seq
    try {
      const data = await listDir(path, { offset, limit: LIST_CHUNK })
      if (mySeq !== seq || !data.isDir) return // 目录已切换:丢弃过期结果
      total = data.total || total
      chunks = new Map(chunks).set(offset, data.entries || [])
    } catch {
      // 失败静默:移除在途标记,滚动回到该区域时自然重试
      requested = new Set([...requested].filter((o) => o !== offset))
    }
  }

  // 目录切换由 App 的 {#key path} 重建本组件处理,无需重置 effect。
  // (曾经的 $effect 依赖 props 解构会陷入更新循环,详见 git 历史)

  // 虚拟滚动窗口:重算可视区间(滚动、挂载、容器尺寸变化时调用)
  function updateWindow() {
    const el = scroller
    if (!el) return
    const st = el.scrollTop
    const ch = el.clientHeight
    start = Math.floor(st / ROW_H)
    end = Math.min(total, Math.ceil((st + ch) / ROW_H) + OVERSCAN)
  }
  const onScroll = updateWindow

  $effect(() => {
    const el = scroller
    if (!el) return
    el.scrollTop = 0 // 组件重建后回到顶部
    updateWindow()
    // 容器尺寸变化(窗口缩放)时重算可视行数
    const ro = new ResizeObserver(updateWindow)
    ro.observe(el)
    return () => ro.disconnect()
  })

  // 可视区覆盖检查:滚动或某页加载完成后,补齐缺失的页(防抖合并快速滚动)
  $effect(() => {
    const el = scroller
    const n = total
    if (!el || n === 0) return
    const missing = []
    for (let i = start; i < end; i += LIST_CHUNK) {
      const ci = Math.floor(i / LIST_CHUNK) * LIST_CHUNK
      if (!chunks.has(ci) && !requested.has(ci)) missing.push(ci)
    }
    if (!missing.length) return
    clearTimeout(fetchTimer)
    fetchTimer = setTimeout(() => missing.forEach(fetchChunk), 80)
    return () => clearTimeout(fetchTimer)
  })
</script>

{#if total === 0}
  <div class="hint">此目录为空</div>
{:else}
  <div class="flex h-full min-h-0 flex-col">
    <div class="grid grid-cols-[minmax(0,1fr)_110px_160px_40px] border-b border-edge bg-panel px-3 text-left text-xs font-semibold uppercase tracking-wider text-muted">
      <div class="py-[7px]">名称</div>
      <div class="py-[7px]">大小</div>
      <div class="py-[7px]">修改时间</div>
      <div class="py-[7px] text-right">操作</div>
    </div>
    <div class="min-h-0 flex-1 overflow-auto" bind:this={scroller} onscroll={onScroll}>
      <div class="relative" style="height: {total * ROW_H}px">
        {#each visible as row, gi (start + gi)}
          {#if row}
            <div
              class="absolute inset-x-0 grid cursor-pointer grid-cols-[minmax(0,1fr)_110px_160px_40px] items-center border-b border-edge px-3 hover:bg-hover"
              style="top: {(start + gi) * ROW_H}px; height: {ROW_H}px"
              role="row"
              onclick={() => onNavigate(row.name)}
            >
              <div class="flex min-w-0 items-center gap-2">
                {#if row.isDir}
                  <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>
                {:else}
                  <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /></svg>
                {/if}
                <span class="truncate" title={row.name}>{row.name}</span>
              </div>
              <div class="text-muted">{row.isDir && !row.isArchive ? '-' : formatSize(row.size)}</div>
              <div class="text-muted">{formatDate(row.modTime)}</div>
              <div class="flex justify-end">
                <a
                  class="inline-flex h-7 w-7 items-center justify-center rounded text-muted no-underline hover:bg-hover hover:text-fg"
                  title="下载"
                  href={downloadUrl(entryPath(row.name))}
                  onclick={(e) => e.stopPropagation()}
                >
                  <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><polyline points="7 10 12 15 17 10" /><line x1="12" y1="15" x2="12" y2="3" /></svg>
                </a>
              </div>
            </div>
          {:else}
            <!-- 未加载的页:占位行保持滚动位置稳定,页加载完成后替换 -->
            <div
              class="absolute inset-x-0 border-b border-edge"
              style="top: {(start + gi) * ROW_H}px; height: {ROW_H}px"
              aria-hidden="true"
            ></div>
          {/if}
        {/each}
      </div>
    </div>
  </div>
{/if}
