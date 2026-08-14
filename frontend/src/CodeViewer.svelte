<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { codeLanguage, copyText, detectLanguage, extension, languageFromMime, languageLabel, TEXT } from './viewers.js'
  import { isDark } from './theme.svelte.js'

  // svelte-jsoneditor is heavy (~600KB): load it only when a JSON file is viewed
  let JsonEditor = $state(null)
  async function loadJsonEditor() {
    if (JsonEditor) return
    JsonEditor = (await import('svelte-jsoneditor')).JSONEditor
  }

  const MAX_SIZE = 5 * 1024 * 1024

  // lang:用户在类型弹窗中手动指定的 hljs 语言(空=按名字/内容自动判断),
  // 优先于 codeLanguage/mime/内容检测。
  let { path, name, mime, lang = '' } = $props()

  let html = $state('')
  let lineNums = $state([])
  let text = $state('')
  let tooBig = $state(false)
  let error = $state('')
  let copied = $state(false)
  let langLabel = $state('')
  let isJson = $state(false)
  let mode = $state('code') // 'code' | 'tree'
  let treeContent = $derived({ text })

  $effect(() => {
    void lang // 同步读取以建立依赖:forced 语言在 load 的 await 之后才读取,
    // 不在此处读取的话,指定类型切换语言不会触发重载
    load()
  })

  async function load() {
    html = ''
    lineNums = []
    text = ''
    tooBig = false
    error = ''
    copied = false
    langLabel = ''
    isJson = false
    mode = 'code'
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
      const forced = lang && hljs.getLanguage(lang) ? lang : ''
      if (forced) {
        html = hljs.highlight(text, { language: forced, ignoreIllegals: true }).value
        langLabel = languageLabel(forced)
        isJson = forced === 'json'
      } else if (codeLanguage(name)) {
        // known name: trust the mapping (用名字解析出的语言,不是 prop lang:
        // 后者只在手动指定时非空,空串会触发 hljs "Unknown language")
        const byName = codeLanguage(name)
        html = hljs.highlight(text, { language: byName, ignoreIllegals: true }).value
        langLabel = languageLabel(byName)
        isJson = byName === 'json'
      } else if (languageFromMime(mime)) {
        // content sniffing pinned the language (e.g. application/json)
        const fromMime = languageFromMime(mime)
        html = hljs.highlight(text, { language: fromMime, ignoreIllegals: true }).value
        langLabel = languageLabel(fromMime)
        isJson = fromMime === 'json'
      } else if (TEXT.includes(extension(name)) || TEXT.includes(name.toLowerCase())) {
        // 纯文本族(txt/log/gitignore/license…):名字即类型,跳过内容检测。
        // 否则 hljs 会把 .gitignore 的 `*`/`!` 误判为 YAML、把含 `#` 的
        // 文本误判为 Markdown 等。
        html = hljs.highlight(text, { language: 'plaintext', ignoreIllegals: true }).value
        langLabel = languageLabel('plaintext')
      } else {
        // no name or mime hint: let the content decide the language, but
        // only among a curated set and only with enough sample to go on
        const detected = detectLanguage(text)
        html = hljs.highlight(text, { language: detected, ignoreIllegals: true }).value
        langLabel = languageLabel(detected)
      }
    } catch (e) {
      error = e.message
    }
  }

  async function toggleMode() {
    if (mode === 'code') {
      mode = 'tree'
      await loadJsonEditor()
    } else {
      mode = 'code'
    }
  }

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
      {#if langLabel}<span class="filetype">{langLabel}</span>{/if}
    </span>
    {#if !tooBig && !error}
      <span class="toolbar-actions">
        {#if isJson}
          <button class="btn" onclick={toggleMode}>
            {mode === 'code' ? '树形视图' : '代码视图'}
          </button>
        {/if}
        <button class="btn" onclick={copy}>{copied ? '已复制' : '复制'}</button>
      </span>
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
    {#if isJson && mode === 'tree'}
      <div class="json-tree" class:jse-theme-dark={isDark()}>
        {#if JsonEditor}
          <JsonEditor content={treeContent} readOnly={true} mode="tree" mainMenuBar={false} statusBar={false} />
        {:else}
          <div class="hint">加载中…</div>
        {/if}
      </div>
    {:else}
      <div class="code-wrap">
        <div class="gutter">
          {#each lineNums as n}<span>{n}</span>{/each}
        </div>
        <pre><code>{@html html}</code></pre>
      </div>
    {/if}
  {/if}
</div>
