<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { copyText } from './viewers.js'
  import { isDark } from './theme.svelte.js'

  // marked/katex/mermaid are heavy: load them lazily, only when a markdown
  // file is actually opened (plain file browsing stays untouched).
  let markedInstance = null
  let katex = null

  function escapeHtml(s) {
    return s
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
  }

  function slugify(text) {
    return text
      .replace(/<[^>]*>/g, '') // drop inline markup from the id
      .toLowerCase()
      .replace(/[^\p{L}\p{N}\s-]/gu, '')
      .trim()
      .replace(/\s+/g, '-')
  }

  const headingIds = new Map()

  // Resolve a relative link/image target against the directory of the
  // markdown file being viewed, collapsing ./ and ../ segments.
  function resolveRel(href) {
    const clean = href.split('#')[0].split('?')[0]
    let decoded
    try {
      decoded = decodeURIComponent(clean)
    } catch {
      decoded = clean
    }
    const i = currentPath.lastIndexOf('/')
    const dir = i > 0 ? currentPath.slice(0, i) : ''
    const out = []
    for (const seg of (dir ? dir.split('/') : []).concat(decoded.split('/'))) {
      if (!seg || seg === '.') continue
      if (seg === '..') {
        out.pop()
        continue
      }
      out.push(seg)
    }
    return out.join('/')
  }

  function escapeAttr(s) {
    return escapeHtml(s)
  }

  // Inline math: $$...$$ renders display mode, $...$ inline mode. A single
  // extension handles both, so block-level $$ never fights the paragraph
  // tokenizer (text like "$$x$$" inside a line renders as display math too,
  // which matches common markdown-math conventions).
  const inlineMath = {
    name: 'inlineMath',
    level: 'inline',
    start(src) {
      const i = src.indexOf('$')
      return i === -1 ? undefined : i
    },
    tokenizer(src) {
      const match = /^\$\$([\s\S]+?)\$\$|^\$((?:\\.|[^$\\])+?)\$/.exec(src)
      if (!match) return undefined
      const [raw, display, inline] = match
      return { type: 'inlineMath', raw, text: display || inline, displayMode: !!display }
    },
    renderer(token) {
      try {
        return katex.renderToString(token.text, { throwOnError: false, displayMode: token.displayMode })
      } catch {
        return token.raw
      }
    }
  }

  const renderer = {
    code({ text, lang }) {
      const info = (lang || '').split(/\s+/)[0].toLowerCase()
      if (info === 'mermaid') {
        // data-src 保存源码:主题切换重渲染时恢复用
        const src = escapeHtml(text)
        return `<div class="mermaid" data-src="${src}">${src}</div>`
      }
      const language = info && hljs.getLanguage(info) ? info : ''
      const highlighted = language
        ? hljs.highlight(text, { language, ignoreIllegals: true }).value
        : hljs.highlightAuto(text).value
      const cls = language ? ` class="hljs language-${language}"` : ' class="hljs"'
      return `<pre><code${cls}>${highlighted}</code></pre>`
    },
    heading({ tokens, depth }) {
      const text = this.parser.parseInline(tokens)
      let id = slugify(text) || 'section'
      const n = headingIds.get(id) || 0
      headingIds.set(id, n + 1)
      if (n > 0) id += '-' + n
      return `<h${depth} id="${id}">${text}</h${depth}>`
    },
    link({ href, title, tokens }) {
      const text = this.parser.parseInline(tokens)
      const h = href || ''
      if (/^(https?:|mailto:|data:)/i.test(h)) {
        return `<a href="${escapeAttr(h)}" target="_blank" rel="noopener noreferrer">${text}</a>`
      }
      if (h.startsWith('#')) return `<a href="${escapeAttr(h)}">${text}</a>`
      const t = title ? ` title="${escapeAttr(title)}"` : ''
      // relative link: navigate inside viewit via the hash router
      const encoded = encodeURIComponent(resolveRel(h)).replace(/%2F/g, '/')
      return `<a href="#/${encoded}"${t}>${text}</a>`
    },
    image({ href, title, tokens }) {
      const alt = this.parser.parseInline(tokens)
      const h = href || ''
      const src = /^(https?:|data:)/i.test(h) ? h : fileUrl(resolveRel(h))
      const t = title ? ` title="${escapeAttr(title)}"` : ''
      return `<img src="${escapeAttr(src)}" alt="${escapeAttr(alt.replace(/<[^>]*>/g, ''))}"${t} loading="lazy">`
    }
  }

  async function ensureLibs() {
    if (markedInstance) return
    const [markedMod, katexMod] = await Promise.all([import('marked'), import('katex')])
    await import('katex/dist/katex.min.css')
    katex = katexMod.default
    markedInstance = markedMod.marked
    markedInstance.use({ gfm: true, async: false, extensions: [inlineMath], renderer })
  }

  // directory of the file currently being rendered, used by resolveRel
  let currentPath = ''

  const MAX_SIZE = 5 * 1024 * 1024

  let { path, name } = $props()

  let html = $state('')
  let sourceHtml = $state('') // markdown-highlighted source for the left pane
  let lineNums = $state([])
  let text = $state('')
  let error = $state('')
  let tooBig = $state(false)
  let copied = $state(false)
  let showSource = $state(true)
  let showPreview = $state(true)
  let previewEl

  $effect(() => {
    load()
  })

  async function load() {
    html = ''
    sourceHtml = ''
    lineNums = []
    text = ''
    error = ''
    tooBig = false
    copied = false
    currentPath = path
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
      const parts = text.split('\n')
      if (parts[parts.length - 1] === '') parts.pop() // trailing newline is not a line
      lineNums = Array.from({ length: parts.length }, (_, i) => i + 1)
      sourceHtml = hljs.highlight(text, { language: 'markdown', ignoreIllegals: true }).value
      await ensureLibs()
      headingIds.clear()
      html = markedInstance.parse(text)
    } catch (e) {
      error = e.message
    }
  }

  // Render mermaid diagrams once the HTML is in the DOM. The effect also
  // re-runs when the preview pane is unfolded (previewEl goes null -> el)
  // and when the theme changes (isDark), re-rendering existing diagrams.
  $effect(() => {
    const h = html
    const dark = isDark()
    if (!h || !previewEl) return
    const nodes = previewEl.querySelectorAll('.mermaid')
    if (nodes.length === 0) return
    let cancelled = false
    ;(async () => {
      const mermaid = (await import('mermaid')).default
      if (cancelled) return
      mermaid.initialize({ startOnLoad: false, theme: dark ? 'dark' : 'neutral', securityLevel: 'strict' })
      // 恢复原始源码后再渲染:已渲染的 SVG 无法直接换主题;
      // data-processed 是 mermaid 渲染过的标记,不清理会被 run 跳过
      for (const n of nodes) {
        const src = n.dataset.src
        if (src !== undefined) n.textContent = src
        n.removeAttribute('data-processed')
      }
      try {
        await mermaid.run({ nodes: [...nodes] })
      } catch {
        // a diagram with a syntax error keeps its raw source visible
      }
    })()
    return () => {
      cancelled = true
    }
  })

  async function copy() {
    if (await copyText(text)) {
      copied = true
      setTimeout(() => (copied = false), 1500)
    }
  }
</script>

<div class="viewer flex flex-col">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      <span class="filetype">Markdown</span>
    </span>
    {#if !tooBig && !error}
      <div class="toolbar-actions">
        <button
          type="button"
          class="btn ghost toggle-btn"
          class:active={showSource}
          title="显示/隐藏源码"
          onclick={() => (showSource = !showSource)}
        >源码</button>
        <button
          type="button"
          class="btn ghost toggle-btn"
          class:active={showPreview}
          title="显示/隐藏预览"
          onclick={() => (showPreview = !showPreview)}
        >预览</button>
        <button type="button" class="btn" onclick={copy}>{copied ? '已复制' : '复制源码'}</button>
      </div>
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
    <div class="flex min-h-0 flex-1">
      {#if showSource}
        <div class="flex min-w-0 flex-1 border-r border-edge">
          <div class="code-wrap min-w-0">
            <div class="gutter">
              {#each lineNums as n}<span>{n}</span>{/each}
            </div>
            <!-- soft-wrap the source so the left pane only ever scrolls vertically -->
            <pre class="[white-space:pre-wrap] [word-break:break-word]"><code>{@html sourceHtml}</code></pre>
          </div>
        </div>
      {/if}
      {#if showPreview}
        <div class="min-w-0 flex-1 overflow-auto" bind:this={previewEl}>
          <div class="md-body">{@html html}</div>
        </div>
      {/if}
    </div>
  {/if}
</div>
