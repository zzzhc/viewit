<script>
  import { onMount } from 'svelte'
  import { setQuery, subscribe, highlightChunks } from './finder.js'

  let { onOpen, base } = $props()

  let open = $state(false)
  let query = $state('')
  let scope = $state('') // 打开面板时快照的当前目录,限制查找范围
  let results = $state([]) // { path, isDir, parts }
  let indexed = $state(0)
  let scopeCount = $state(0) // 查找范围内条目数
  let partial = $state(false)
  let matched = $state(0)
  let truncated = $state(false)
  let active = $state(0)
  let inputEl = $state(undefined)
  let listEl = $state(undefined)
  let debounce = 0

  // 三次 Shift 检测
  let shiftCount = 0
  let lastShift = 0
  const SHIFT_WINDOW = 700 // ms

  function openFinder() {
    open = true
    query = ''
    results = []
    indexed = 0
    scopeCount = 0
    partial = false
    matched = 0
    truncated = false
    active = 0
    scope = base && base !== '/' ? base : '' // 快照当前目录作为查找范围,"/"=全量
    setQuery('', scope)
    requestAnimationFrame(() => inputEl?.focus())
  }

  function closeFinder() {
    open = false
    query = ''
    setQuery('', scope)
  }

  function pick(r) {
    closeFinder()
    onOpen(r.path)
  }

  function onKeydown(e) {
    if (open) {
      if (e.key === 'Escape') {
        e.preventDefault()
        closeFinder()
      } else if (e.key === 'ArrowDown') {
        e.preventDefault()
        active = Math.min(active + 1, results.length - 1)
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        active = Math.max(active - 1, 0)
      } else if (e.key === 'Enter' && results[active]) {
        e.preventDefault()
        pick(results[active])
      } else if (e.ctrlKey && (e.key === 'p' || e.key === 'P')) {
        e.preventDefault()
        inputEl?.focus()
      }
      return
    }
    // 全局快捷键:Ctrl+P 或快速连按三次 Shift
    if (e.ctrlKey && (e.key === 'p' || e.key === 'P')) {
      e.preventDefault()
      openFinder()
      return
    }
    if (e.key === 'Shift') {
      if (e.repeat) return
      const now = performance.now()
      shiftCount = now - lastShift > SHIFT_WINDOW ? 1 : shiftCount + 1
      lastShift = now
      if (shiftCount >= 3) {
        shiftCount = 0
        openFinder()
      }
    } else {
      shiftCount = 0
    }
  }

  $effect(() => {
    if (!open) return
    const q = query // 同步读取以建立依赖,回调内读取不会被跟踪
    const sc = scope
    clearTimeout(debounce)
    debounce = setTimeout(() => {
      setQuery(q, sc)
      active = 0
    }, 100)
  })

  $effect(() => {
    if (open && listEl) {
      listEl.querySelector('.sel-row')?.scrollIntoView({ block: 'nearest' })
    }
  })

  onMount(() => {
    window.addEventListener('keydown', onKeydown)
    const unsub = subscribe((msg) => {
      results = (msg.matches || []).map((m) => ({
        path: m.path,
        isDir: m.isDir,
        parts: highlightChunks(m.path, m.marks || [])
      }))
      indexed = msg.indexed
      scopeCount = msg.scopeCount || 0
      partial = msg.partial
      matched = msg.matched || 0
      truncated = !!msg.truncated
      active = Math.min(active, Math.max(results.length - 1, 0))
    })
    return () => {
      window.removeEventListener('keydown', onKeydown)
      unsub()
      clearTimeout(debounce)
    }
  })
</script>

{#if open}
  <div class="fixed inset-0 z-[100] flex justify-center bg-[var(--vt-overlay)] pt-[12vh]" role="dialog" aria-modal="true" aria-label="文件查找">
    <div class="flex max-h-[65vh] w-[min(680px,92vw)] flex-col overflow-hidden rounded-lg border border-edge bg-panel shadow-[0_16px_48px_rgba(0,0,0,0.5)]">
      <input
        bind:this={inputEl}
        bind:value={query}
        class="m-2.5 rounded-md border border-edge bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
        placeholder={scope ? `在 ${scope}/ 中查找` : '输入关键字查找文件'}
        spellcheck="false"
        autocomplete="off"
      />
      <div class="px-3.5 pb-1.5">
        {#if partial}
          <span class="text-xs text-muted">索引中… 已收录 {indexed} 项</span>
        {:else if query}
          <span class="text-xs text-muted">
            共 {scopeCount} 项，匹配 {matched} 条{truncated ? `，仅显示前 ${results.length}` : ''}
          </span>
        {:else}
          <span class="text-xs text-muted">
            {scope ? `在 ${scope}/ 内查找` : '全库查找'} · 共 {scopeCount} 项 · Ctrl+P / 三次 Shift 打开
          </span>
        {/if}
      </div>
      <div class="min-h-[60px] overflow-y-auto border-t border-edge py-1 pb-2" bind:this={listEl}>
        {#if results.length}
          {#each results as r, i (r.path)}
            <div
              class="flex cursor-pointer items-center gap-2 whitespace-nowrap px-3 py-[5px] hover:bg-hover"
              class:sel-row={i === active}
              onmouseenter={() => (active = i)}
              onclick={() => pick(r)}
            >
              {#if r.isDir}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" /></svg>
              {:else}
                <svg class="flex-none text-muted" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline points="14 2 14 8 20 8" /></svg>
              {/if}
              <span class="finder-path truncate">
                {#each r.parts as p, pi (pi)}
                  {#if p.hit}
                    <mark>{p.t}</mark>
                  {:else}
                    <span class={p.dir ? 'text-muted' : 'text-fg'}>{p.t}</span>
                  {/if}
                {/each}
              </span>
            </div>
          {/each}
        {:else if query && !partial}
          <div class="px-3.5 py-2.5 text-xs text-muted">无匹配</div>
        {/if}
      </div>
    </div>
  </div>
{/if}
