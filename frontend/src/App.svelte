<script>
  import { onMount } from 'svelte'
  import FileList from './FileList.svelte'
  import FileFinder from './FileFinder.svelte'
  import CodeViewer from './CodeViewer.svelte'
  import ImageViewer from './ImageViewer.svelte'
  import VideoViewer from './VideoViewer.svelte'
  import AudioViewer from './AudioViewer.svelte'
  import PdfViewer from './PdfViewer.svelte'
  import XmlViewer from './XmlViewer.svelte'
  import MarkdownViewer from './MarkdownViewer.svelte'
  import HtmlViewer from './HtmlViewer.svelte'
  import DownloadViewer from './DownloadViewer.svelte'
  import { listDir } from './api.js'
  import { downloadUrl } from './api.js'
  import { viewerFor } from './viewers.js'
  import { cycleTheme, themeLabel, themePref } from './theme.svelte.js'

  // path is relative to root: "/" or "sub" / "sub/inner.txt"
  let path = $state('/')
  let entries = $state([])
  let selected = $state(null) // file entry being previewed
  let loading = $state(true)
  let error = $state('')

  let segs = $derived(path === '/' ? [] : path.split('/').filter(Boolean))
  // when load() resolves path to a file, path IS the file's full path
  let filePath = $derived(selected ? path : '')
  let view = $derived(selected ? viewerFor(selected.name, selected.mime) : '')

  function navigate(p) {
    if (!p || p === '/') {
      location.hash = '#/'
    } else {
      location.hash = '#/' + p.split('/').map(encodeURIComponent).join('/')
    }
  }

  function pathFromHash() {
    const h = location.hash
    if (!h || h === '#' || h === '#/') return '/'
    return h
      .slice(2)
      .split('/')
      .map(decodeURIComponent)
      .join('/')
  }

  async function load() {
    loading = true
    error = ''
    try {
      const data = await listDir(path)
      if (data.isDir) {
        // 空目录的响应没有 entries 字段(omitempty 省略空 slice)
        entries = data.entries || []
        selected = null
      } else {
        selected = data.file
        entries = []
      }
    } catch (e) {
      error = e.message
      entries = []
      selected = null
    } finally {
      loading = false
    }
  }

  function onNavigate(name) {
    // dirs and files alike: push the joined path into the hash; load() below
    // resolves it to a listing or a file preview
    navigate(segs.concat(name).join('/'))
  }

  function onHashChange() {
    // hashes that are not routes ("#section" in-page anchors) belong to the
    // browser's default scroll behavior, not to the file router
    if (!location.hash.startsWith('#/')) return
    path = pathFromHash()
  }

  // 文件查找范围:目录页传当前目录,文件预览页取其父目录
  function finderBase() {
    if (!selected) return path
    const i = path.lastIndexOf('/')
    return i > 0 ? path.slice(0, i) : ''
  }

  onMount(() => {
    path = pathFromHash()
    window.addEventListener('hashchange', onHashChange)
    return () => window.removeEventListener('hashchange', onHashChange)
  })

  $effect(() => {
    // reload whenever the routed path changes
    load()
  })

  $effect(() => {
    if (selected) document.title = selected.name
    else if (path === '/') document.title = 'viewit'
    else document.title = segs[segs.length - 1]
  })
</script>

<div class="flex h-screen flex-col">
  <header class="flex flex-none items-center gap-4 border-b border-edge bg-panel px-4 py-2.5">
    <button
      type="button"
      class="cursor-pointer select-none border-0 bg-transparent font-sans text-base text-accent"
      onclick={() => navigate('/')}
    >viewit</button>
    <nav class="flex min-w-0 flex-wrap items-center gap-1 overflow-hidden text-[13px]">
      <button
        type="button"
        class="cursor-pointer whitespace-nowrap border-0 bg-transparent p-0 font-sans text-[13px] text-accent hover:underline"
        onclick={() => navigate('/')}
      >/</button>
      {#each segs as seg, i}
        <button
          type="button"
          class="cursor-pointer whitespace-nowrap border-0 bg-transparent p-0 font-sans text-[13px] text-accent hover:underline"
          onclick={() => navigate(segs.slice(0, i + 1).join('/'))}
        >{seg}</button>
        <span class="text-muted">/</span>
      {/each}
    </nav>
    {#if selected}
      <a class="btn" href={downloadUrl(path)} download={selected.name}>下载</a>
    {/if}
    <button
      type="button"
      class="theme-btn ml-auto flex flex-none cursor-pointer items-center justify-center rounded border border-edge bg-transparent p-1.5 text-muted hover:bg-hover hover:text-fg"
      title={'主题：' + themeLabel() + '（点击切换）'}
      onclick={cycleTheme}
    >
      {#if themePref() === 'light'}
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="5" /><line x1="12" y1="1" x2="12" y2="3" /><line x1="12" y1="21" x2="12" y2="23" /><line x1="4.22" y1="4.22" x2="5.64" y2="5.64" /><line x1="18.36" y1="18.36" x2="19.78" y2="19.78" /><line x1="1" y1="12" x2="3" y2="12" /><line x1="21" y1="12" x2="23" y2="12" /><line x1="4.22" y1="19.78" x2="5.64" y2="18.36" /><line x1="18.36" y1="5.64" x2="19.78" y2="4.22" /></svg>
      {:else if themePref() === 'dark'}
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" /></svg>
      {:else}
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2" y="3" width="20" height="14" rx="2" ry="2" /><line x1="8" y1="21" x2="16" y2="21" /><line x1="12" y1="17" x2="12" y2="21" /></svg>
      {/if}
    </button>
  </header>

  <main class="min-h-0 flex-1 overflow-auto">
    {#if loading}
      <div class="hint">加载中…</div>
    {:else if error}
      <div class="hint error">
        <p>{error}</p>
        <button class="btn" onclick={load}>重试</button>
      </div>
    {:else if selected}
      {#if view === 'image'}
        <ImageViewer path={filePath} />
      {:else if view === 'video'}
        <VideoViewer path={filePath} />
      {:else if view === 'audio'}
        <AudioViewer path={filePath} />
      {:else if view === 'pdf'}
        <PdfViewer path={filePath} />
      {:else if view === 'xml'}
        <XmlViewer path={filePath} name={selected.name} />
      {:else if view === 'code'}
        <CodeViewer path={filePath} name={selected.name} mime={selected.mime} />
      {:else if view === 'markdown'}
        <MarkdownViewer path={filePath} name={selected.name} />
      {:else if view === 'html'}
        <HtmlViewer path={filePath} name={selected.name} />
      {:else}
        <DownloadViewer path={filePath} name={selected.name} size={selected.size} />
      {/if}
    {:else}
      <FileList {entries} {path} onNavigate={onNavigate} />
    {/if}
  </main>

  <FileFinder onOpen={(p) => navigate(p)} base={finderBase()} />
</div>
