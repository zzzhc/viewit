<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { codeLanguage, copyText, detectLanguage, languageFromMime, languageLabel } from './viewers.js'

  // svelte-jsoneditor is heavy (~600KB): load it only when a JSON file is viewed
  let JsonEditor = $state(null)
  async function loadJsonEditor() {
    if (JsonEditor) return
    JsonEditor = (await import('svelte-jsoneditor')).JSONEditor
  }

  const MAX_SIZE = 5 * 1024 * 1024

  let { path, name, mime } = $props()

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
      const lang = codeLanguage(name)
      if (lang) {
        // known name: trust the mapping
        html = hljs.highlight(text, { language: lang, ignoreIllegals: true }).value
        langLabel = languageLabel(lang)
        isJson = lang === 'json'
      } else if (languageFromMime(mime)) {
        // content sniffing pinned the language (e.g. application/json)
        const fromMime = languageFromMime(mime)
        html = hljs.highlight(text, { language: fromMime, ignoreIllegals: true }).value
        langLabel = languageLabel(fromMime)
        isJson = fromMime === 'json'
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

<div class="viewer code-viewer">
  <div class="viewer-toolbar">
    <span class="toolbar-left">
      <span class="filename">{name}</span>
      {#if langLabel}<span class="filetype">{langLabel}</span>{/if}
    </span>
    {#if !tooBig && !error}
      {#if isJson}
        <button class="btn" onclick={toggleMode}>
          {mode === 'code' ? '树形视图' : '代码视图'}
        </button>
      {/if}
      <button class="btn" onclick={copy}>{copied ? '已复制' : '复制'}</button>
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
      <div class="json-tree">
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
