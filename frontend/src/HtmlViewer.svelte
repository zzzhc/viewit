<script>
  import hljs from 'highlight.js'
  import { fileUrl, rawUrl } from './api.js'
  import { copyText } from './viewers.js'

  const MAX_SIZE = 5 * 1024 * 1024

  let { path, name } = $props()

  let html = $state('')
  let lineNums = $state([])
  let text = $state('')
  let error = $state('')
  let tooBig = $state(false)
  let copied = $state(false)
  let showSource = $state(true)
  let showPreview = $state(true)
  let fixed = $state(false) // 绝对路径修复模式
  let fixedHtml = $state('')

  $effect(() => {
    load()
  })

  // 修复开关跟随文件切换重置,但不被 load() 的异步完成覆盖
  // (否则在加载期间点击按钮会被 load 尾部重置掉)
  $effect(() => {
    path
    fixed = false
    fixedHtml = ''
  })

  // 页面内 "/xxx" 形式的绝对路径指向部署站点的根,在预览环境中会解析到
  // viewit 的根而不是文件所在目录,导致 assets 等资源加载失败。修复模式
  // 把它们重写为镜像路径 /api/raw/<文件目录>/xxx,并以 srcdoc 注入预览。
  function rewriteHtml(source, dir) {
    const doc = new DOMParser().parseFromString(source, 'text/html')
    const prefix = '/api/raw' + (dir ? '/' + dir : '')
    const isExternal = (u) =>
      u.startsWith('//') || /^(https?:|data:|mailto:|tel:|javascript:|about:|#)/i.test(u)

    function fixUrl(u) {
      if (!u) return u
      const t = u.trim()
      if (!t.startsWith('/') || isExternal(t)) return u
      return prefix + t
    }

    function fixSrcset(s) {
      return s
        .split(',')
        .map((part) => {
          const m = part.trim().match(/^(\S+)(\s+.*)?$/)
          if (!m) return part
          const u = fixUrl(m[1])
          return u === m[1] ? part : u + (m[2] || '')
        })
        .join(', ')
    }

    function fixCssUrls(css) {
      return css.replace(/url\(\s*(['"]?)(\/[^'")]+)\1\s*\)/gi, (m, q, u) => {
        if (u.startsWith('//')) return m // 协议相对 URL 指向外部站点,不动
        return `url(${q}${prefix}${u}${q})`
      })
    }

    // 重写所有绝对路径属性
    for (const el of doc.querySelectorAll('*')) {
      for (const attr of ['src', 'href', 'data', 'poster', 'action']) {
        if (el.hasAttribute(attr)) el.setAttribute(attr, fixUrl(el.getAttribute(attr)))
      }
      if (el.hasAttribute('srcset')) el.setAttribute('srcset', fixSrcset(el.getAttribute('srcset')))
      if (el.hasAttribute('style')) el.setAttribute('style', fixCssUrls(el.getAttribute('style')))
      // Vite 等构建产物会给 script/link 加 crossorigin 属性;它要求资源以
      // CORS 模式加载,而预览运行在 opaque origin 下拿不到 ACAO 响应头,
      // 模块会被浏览器拒载。预览环境里移除它,资源以 no-cors 模式正常加载。
      el.removeAttribute('crossorigin')
    }
    for (const st of doc.querySelectorAll('style')) {
      st.textContent = fixCssUrls(st.textContent)
    }

    // srcdoc 文档的基准 URL 是父页面,必须注入 base 指向文件目录,
    // 相对路径与漏网的属性才解析正确。
    const base = doc.querySelector('base[href]')
    if (base) {
      const h = base.getAttribute('href').trim()
      if (h && !isExternal(h)) base.setAttribute('href', prefix + '/' + h.replace(/^\/+/, ''))
    } else {
      const b = doc.createElement('base')
      b.setAttribute('href', prefix + '/')
      doc.head.insertBefore(b, doc.head.firstChild)
    }

    return new XMLSerializer().serializeToString(doc)
  }

  async function load() {
    html = ''
    lineNums = []
    text = ''
    error = ''
    tooBig = false
    copied = false
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
      html = hljs.highlight(text, { language: 'xml', ignoreIllegals: true }).value
      const i = path.lastIndexOf('/')
      const dir = i > 0 ? path.slice(0, i) : ''
      fixedHtml = rewriteHtml(text, dir)
    } catch (e) {
      error = e.message
    }
  }

  async function copy() {
    if (await copyText(text)) {
      copied = true
      setTimeout(() => (copied = false), 1500)
    }
  }
</script>

<div class="viewer html-viewer">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      <span class="filetype">HTML</span>
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
        <button
          type="button"
          class="btn ghost toggle-btn"
          class:active={fixed}
          title="将页面内 /xxx 绝对路径重写为文件目录下的资源,解决 assets 等加载失败"
          onclick={() => (fixed = !fixed)}
        >修复绝对路径</button>
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
    <div class="html-split">
      {#if showSource}
        <div class="html-source">
          <div class="code-wrap">
            <div class="gutter">
              {#each lineNums as n}<span>{n}</span>{/each}
            </div>
            <pre><code>{@html html}</code></pre>
          </div>
        </div>
      {/if}
      {#if showPreview}
        <div class="html-preview">
          <!-- 缺省不含 allow-same-origin:预览页面运行在独立 opaque origin,
               页面脚本触碰不到 viewit 本体;allow-scripts 等保证渲染效果与
               真实浏览器一致。src 指向镜像路径的 /api/raw,页面内相对资源
               (图片/CSS/JS)按文件自身目录解析;修复模式改用 srcdoc 注入
               重写后的文档。 -->
          {#if fixed}
            <iframe
              title="HTML 预览"
              srcdoc={fixedHtml}
              sandbox="allow-scripts allow-forms allow-modals allow-popups allow-downloads"
              referrerpolicy="no-referrer"
            ></iframe>
          {:else}
            <iframe
              title="HTML 预览"
              src={rawUrl(path)}
              sandbox="allow-scripts allow-forms allow-modals allow-popups allow-downloads"
              referrerpolicy="no-referrer"
            ></iframe>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .html-viewer {
    display: flex;
    flex-direction: column;
  }

  .html-split {
    flex: 1;
    min-height: 0;
    display: flex;
  }

  .html-source {
    flex: 1 1 50%;
    min-width: 0;
    display: flex;
    border-right: 1px solid var(--border);
    overflow: auto;
  }

  .html-source .code-wrap {
    flex: 1;
    min-width: 0;
  }

  /* soft-wrap the source so the left pane only ever scrolls vertically */
  .html-source pre {
    white-space: pre-wrap;
    word-break: break-word;
  }

  .html-preview {
    flex: 1 1 50%;
    min-width: 0;
    overflow: auto;
    background: #fff;
  }

  .html-preview iframe {
    display: block;
    width: 100%;
    height: 100%;
    border: none;
    background: #fff; /* 页面未设背景色时按白底预览 */
  }

  .toggle-btn.active {
    color: var(--accent);
    border-color: var(--accent);
    background: transparent;
  }
</style>
