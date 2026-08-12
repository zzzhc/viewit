<script>
  import { formatSize, formatDate } from './format.js'
  import { downloadUrl } from './api.js'

  let { entries, path, onNavigate } = $props()

  // 固定行高虚拟滚动:只渲染可视区行,大目录(数万行)也能即时出结果、
  // 流畅滚动。ROW_H 必须与行 div 的 style height 一致,否则滚动会跳。
  const ROW_H = 34
  const OVERSCAN = 10 // 可视区上下各多渲染的行数,提前填充滚动缓冲

  let scroller = $state(undefined)
  let start = $state(0)
  let end = $state(0)

  function entryPath(name) {
    return path === '/' ? name : path + '/' + name
  }

  let visible = $derived(entries.slice(start, end))

  $effect(() => {
    const el = scroller
    const n = entries.length
    if (!el) return
    el.scrollTop = 0 // 切换目录回到顶部
    const update = () => {
      const st = el.scrollTop
      const ch = el.clientHeight
      start = Math.floor(st / ROW_H)
      end = Math.min(n, Math.ceil((st + ch) / ROW_H) + OVERSCAN)
    }
    update()
    // 容器尺寸变化(窗口缩放)时重算可视行数
    const ro = new ResizeObserver(update)
    ro.observe(el)
    el.addEventListener('scroll', update, { passive: true })
    return () => {
      ro.disconnect()
      el.removeEventListener('scroll', update)
    }
  })
</script>

{#if entries.length === 0}
  <div class="hint">此目录为空</div>
{:else}
  <div class="flex h-full min-h-0 flex-col">
    <div class="grid grid-cols-[minmax(0,1fr)_110px_160px_40px] border-b border-edge bg-panel px-3 text-left text-xs font-semibold uppercase tracking-wider text-muted">
      <div class="py-[7px]">名称</div>
      <div class="py-[7px]">大小</div>
      <div class="py-[7px]">修改时间</div>
      <div class="py-[7px] text-right">操作</div>
    </div>
    <div class="min-h-0 flex-1 overflow-auto" bind:this={scroller}>
      <div class="relative" style="height: {entries.length * ROW_H}px">
        {#each visible as entry, i (entry.name)}
          <div
            class="absolute inset-x-0 grid cursor-pointer grid-cols-[minmax(0,1fr)_110px_160px_40px] items-center border-b border-edge px-3 hover:bg-hover"
            style="top: {(start + i) * ROW_H}px; height: {ROW_H}px"
            role="row"
            onclick={() => onNavigate(entry.name)}
          >
            <div class="flex min-w-0 items-center gap-2">
              {#if entry.isDir}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>
              {:else}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /></svg>
              {/if}
              <span class="truncate" title={entry.name}>{entry.name}</span>
            </div>
            <div class="text-muted">{entry.isDir ? '-' : formatSize(entry.size)}</div>
            <div class="text-muted">{formatDate(entry.modTime)}</div>
            <div class="flex justify-end">
              <a
                class="inline-flex h-7 w-7 items-center justify-center rounded text-muted no-underline hover:bg-hover hover:text-fg"
                title="下载"
                href={downloadUrl(entryPath(entry.name))}
                onclick={(e) => e.stopPropagation()}
              >
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><polyline points="7 10 12 15 17 10" /><line x1="12" y1="15" x2="12" y2="3" /></svg>
              </a>
            </div>
          </div>
        {/each}
      </div>
    </div>
  </div>
{/if}
