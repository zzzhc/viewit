<script>
  import { onMount } from 'svelte'
  import { FILE_TYPES, viewTypeLabel, languageLabel } from './viewers.js'

  // props:autoLabel 自动识别出的查看类型中文名;active 当前指定类型条目
  // (null=未指定);onPick(entry) 选择类型;onClose 关闭。
  let { autoLabel, active, onPick, onClose } = $props()

  let query = $state('')
  let inputEl = $state(undefined)

  // 组顺序即展示顺序;download 组固定垫底(不是真实文件类型)
  const GROUPS = [
    ['image', '图片'],
    ['video', '视频'],
    ['audio', '音频'],
    ['pdf', 'PDF'],
    ['xml', 'XML'],
    ['markdown', 'Markdown'],
    ['html', 'HTML'],
    ['jsonl', 'JSONL'],
    ['sqlite', 'SQLite'],
    ['code', '代码/文本']
  ]

  const q = $derived(query.trim().toLowerCase())

  // 条目副标:代码类显示语言名(如 ".go → Go"),其余为空
  const itemHint = (t) => (t.view === 'code' && t.lang ? languageLabel(t.lang) : '')

  // 搜索过滤:名称(go、go.mod)、语言标签(Go)、组名(代码)任一命中即可
  const grouped = $derived.by(() => {
    const out = []
    for (const [view, label] of GROUPS) {
      const items = FILE_TYPES.filter(
        (t) =>
          t.view === view &&
          (!q ||
            t.name.includes(q) ||
            itemHint(t).toLowerCase().includes(q) ||
            label.toLowerCase().includes(q))
      )
      if (items.length) out.push({ label, items })
    }
    if (!q || '下载'.includes(q) || 'download'.includes(q)) {
      out.push({ label: '下载', items: [{ name: '下载', view: 'download', lang: '' }] })
    }
    return out
  })

  const noMatch = $derived(grouped.length === 0)

  function pick(t) {
    onPick(t)
  }

  function clear() {
    onPick(null)
  }

  function onKeydown(e) {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    }
  }

  onMount(() => {
    window.addEventListener('keydown', onKeydown)
    requestAnimationFrame(() => inputEl?.focus())
    return () => window.removeEventListener('keydown', onKeydown)
  })
</script>

<div
  class="fixed inset-0 z-[100] flex justify-center bg-[var(--vt-overlay)] pt-[10vh]"
  role="dialog"
  aria-modal="true"
  aria-label="指定文件类型"
  onclick={onClose}
>
  <div
    class="flex max-h-[75vh] w-[min(560px,92vw)] flex-col overflow-hidden rounded-lg border border-edge bg-panel shadow-[0_16px_48px_rgba(0,0,0,0.5)]"
    onclick={(e) => e.stopPropagation()}
  >
    <div class="flex flex-none items-center justify-between px-4 py-2.5">
      <span class="text-sm font-semibold">指定文件类型</span>
      <button
        type="button"
        class="cursor-pointer border-0 bg-transparent p-0.5 text-lg leading-none text-muted hover:text-fg"
        title="关闭"
        onclick={onClose}
      >×</button>
    </div>
    <div class="flex-none px-4 pb-2">
      <input
        bind:this={inputEl}
        bind:value={query}
        class="w-full rounded-md border border-edge bg-bg px-3 py-2 text-sm text-fg outline-none focus:border-accent"
        placeholder="筛选类型,如 go、md、png、Dockerfile…"
        spellcheck="false"
        autocomplete="off"
      />
      <div class="mt-1.5 flex items-center gap-2 text-xs text-muted">
        <span>当前自动识别:{autoLabel}</span>
        {#if active}
          <span>· 已指定 {active.name}</span>
          <button
            type="button"
            class="cursor-pointer border-0 bg-transparent p-0 text-xs text-accent hover:underline"
            onclick={clear}
          >恢复自动</button>
        {/if}
      </div>
    </div>
    <div class="min-h-[120px] overflow-y-auto border-t border-edge p-3">
      {#if noMatch}
        <div class="py-8 text-center text-xs text-muted">无匹配类型</div>
      {:else}
        {#each grouped as g}
          <div class="mb-3 last:mb-0">
            <div class="px-1 pb-1.5 text-xs font-semibold uppercase tracking-wider text-muted">{g.label}</div>
            <div class="flex flex-wrap gap-1.5">
              {#each g.items as t}
                <button
                  type="button"
                  class="cursor-pointer rounded-md border px-2.5 py-1 font-mono text-[12px]"
                  class:border-accent={active && active.view === t.view && active.name === t.name}
                  class:text-accent={active && active.view === t.view && active.name === t.name}
                  class:bg-[var(--vt-selection-soft)]={active && active.view === t.view && active.name === t.name}
                  class:border-edge={!(active && active.view === t.view && active.name === t.name)}
                  title={`${g.label}${itemHint(t) ? ` · ${itemHint(t)}` : ''}`}
                  onclick={() => pick(t)}
                >{t.name}</button>
              {/each}
            </div>
          </div>
        {/each}
      {/if}
    </div>
  </div>
</div>
