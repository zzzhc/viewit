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
  import DownloadViewer from './DownloadViewer.svelte'
  import { listDir } from './api.js'
  import { viewerFor } from './viewers.js'

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
        entries = data.entries
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

<div class="app">
  <header class="topbar">
    <button type="button" class="brand" onclick={() => navigate('/')}>viewit</button>
    <nav class="crumbs">
      <button type="button" class="crumb" onclick={() => navigate('/')}>/</button>
      {#each segs as seg, i}
        <button type="button" class="crumb" onclick={() => navigate(segs.slice(0, i + 1).join('/'))}>{seg}</button>
        <span class="crumb-sep">/</span>
      {/each}
    </nav>
  </header>

  <main class="content">
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
      {:else}
        <DownloadViewer path={filePath} name={selected.name} size={selected.size} />
      {/if}
    {:else}
      <FileList {entries} onNavigate={onNavigate} />
    {/if}
  </main>

  <FileFinder onOpen={(p) => navigate(p)} base={finderBase()} />
</div>
