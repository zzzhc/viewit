<script>
  import hljs from 'highlight.js'
  import { fileUrl } from './api.js'
  import { formatSize } from './format.js'
  import { detectLanguage, languageLabel } from './viewers.js'

  const MAX_SIZE = 5 * 1024 * 1024
  const PROBE_BYTES = 4096

  // force:用户在查看页手动指定了"下载"类型,跳过文本探测直接给下载提示页。
  let { path, name, size, force = false } = $props()

  let ext = $derived(name.includes('.') ? name.split('.').pop().toUpperCase() : '')
  let probing = $state(false)
  let isText = $state(false)
  let html = $state('')
  let text = $state('')
  let tooBig = $state(false)
  let lineNums = $state([])
  let copied = $state(false)
  let langLabel = $state('')

  $effect(() => {
    if (force) {
      // 手动指定"下载":跳过文本探测,清除自动探测的残留状态,直接给下载页。
      probing = false
      isText = false
    } else {
      probe()
    }
  })

  // Unknown extension: sniff the head of the file; when it is plain UTF-8
  // text, render it as a text viewer instead of forcing a download.
  async function probe() {
    probing = true
    isText = false
    html = ''
    text = ''
    tooBig = false
    langLabel = ''
    try {
      const res = await fetch(fileUrl(path), { headers: { Range: `bytes=0-${PROBE_BYTES - 1}` } })
      if (!res.ok) return
      const buf = new Uint8Array(await res.arrayBuffer())
      if (!looksLikeText(buf)) return

      const cr = res.headers.get('content-range')
      const total = cr ? Number(cr.split('/')[1]) : buf.length
      if (total > MAX_SIZE) {
        tooBig = true
        return
      }
      let full = ''
      if (res.status === 206 && buf.length === PROBE_BYTES) {
        const r2 = await fetch(fileUrl(path))
        full = await r2.text()
      } else {
        full = new TextDecoder('utf-8').decode(buf)
      }
      if (full.length > MAX_SIZE) {
        tooBig = true
        return
      }
      const parts = full.split('\n')
      if (parts[parts.length - 1] === '') parts.pop()
      lineNums = Array.from({ length: parts.length }, (_, i) => i + 1)
      text = full
      // no known language for this name: let hljs detect from content
      const detected = detectLanguage(full)
      html = detected === 'plaintext' ? hljs.escapeHTML(full) : hljs.highlight(full, { language: detected, ignoreIllegals: true }).value
      langLabel = languageLabel(detected)
      isText = true
    } catch {
      // network error: fall through to the download view
    } finally {
      probing = false
    }
  }

  function looksLikeText(buf) {
    if (buf.length === 0) return true
    let s
    try {
      s = new TextDecoder('utf-8', { fatal: true }).decode(buf)
    } catch {
      return false
    }
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i)
      if (c === 0) return false
      if (c < 32 && c !== 9 && c !== 10 && c !== 13) return false
    }
    return true
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      copied = true
      setTimeout(() => (copied = false), 1500)
    } catch {
      // clipboard unavailable: fail silently
    }
  }
</script>

{#if probing}
  <div class="hint">检查类型…</div>
{:else if isText}
  <div class="viewer flex flex-col">
    <div class="viewer-toolbar">
      <span class="toolbar-left">
        <span class="filename">{name}</span>
        {#if langLabel}<span class="filetype">{langLabel}</span>{/if}
      </span>
      <button class="btn" onclick={copy}>{copied ? '已复制' : '复制'}</button>
    </div>
    {#if tooBig}
      <div class="hint">
        <p>文件过大(&gt;5MB),请下载查看</p>
        <a class="btn" href={fileUrl(path)} download>下载</a>
      </div>
    {:else}
      <div class="code-wrap">
        <div class="gutter">
          {#each lineNums as n}<span>{n}</span>{/each}
        </div>
        <pre><code>{@html html}</code></pre>
      </div>
    {/if}
  </div>
{:else}
  <div class="viewer flex flex-col items-center justify-center gap-4">
    <p class="m-0 text-lg">{name}</p>
    <p class="hint p-0">
      {#if ext}类型:{ext} · {/if}大小:{formatSize(size)} · 此类型暂不支持在线预览
    </p>
    <a class="btn" href={fileUrl(path)} download={name}>下载</a>
  </div>
{/if}
