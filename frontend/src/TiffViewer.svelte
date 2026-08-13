<script>
  import { onMount } from 'svelte'
  import { fileUrl } from './api.js'
  import UTIF from 'utif'
  import Viewer from 'viewerjs'
  import 'viewerjs/dist/viewer.css'

  // 与代码/XML 查看器一致的字节上限
  const MAX_SIZE = 5 * 1024 * 1024
  // 未压缩 RGBA 内存与解码耗时随像素线性增长,超限拒绝预览(约 120MB/页、240MB 合计)
  const MAX_PAGE_PIXELS = 30_000_000
  const MAX_TOTAL_PIXELS = 60_000_000

  let { path } = $props()

  let error = $state('')
  let tooBig = $state('') // '' | 'size' | 'pixels'
  let source = $state(null)
  let viewer = null
  let urls = [] // 每页 objectURL,普通数组(无需响应式)
  let disposed = false

  $effect(() => {
    load()
  })

  onMount(() => {
    return () => {
      // 组件销毁:撤销 URL 并销毁 viewerjs
      disposed = true
      if (viewer) {
        viewer.destroy()
        viewer = null
      }
      for (const u of urls) URL.revokeObjectURL(u)
      urls = []
    }
  })

  async function load() {
    error = ''
    tooBig = ''
    // 换文件:撤销上一文件的 URL,清空容器,重建 viewerjs
    for (const u of urls) URL.revokeObjectURL(u)
    urls = []
    if (viewer) {
      viewer.destroy()
      viewer = null
    }
    const el = source
    if (el) el.innerHTML = ''
    let buf
    try {
      const res = await fetch(fileUrl(path))
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const len = Number(res.headers.get('content-length') || 0)
      if (len > MAX_SIZE) {
        tooBig = 'size'
        return
      }
      buf = await res.arrayBuffer()
      if (buf.byteLength > MAX_SIZE) {
        tooBig = 'size'
        return
      }
    } catch (e) {
      if (!disposed) error = e.message
      return
    }
    let ifds
    try {
      ifds = UTIF.decode(buf)
    } catch {
      error = '无法解析此 TIFF 文件'
      return
    }
    if (!ifds || !ifds.length) {
      error = '无法解析此 TIFF 文件'
      return
    }
    let total = 0
    for (const ifd of ifds) {
      const px = ifd.width * ifd.height
      total += px
      if (px > MAX_PAGE_PIXELS) {
        tooBig = 'pixels'
        return
      }
    }
    if (total > MAX_TOTAL_PIXELS) {
      tooBig = 'pixels'
      return
    }
    try {
      // 解码 + 每页转 PNG,交给与 ImageViewer 相同的 viewerjs:多页走它的
      // prev/next,缩放/旋转/翻转等交互完全一致
      const imgs = []
      for (const ifd of ifds) {
        if (disposed) return
        UTIF.decodeImage(buf, ifd)
        const url = await rgbaToObjectURL(ifd.width, ifd.height, UTIF.toRGBA8(ifd))
        urls.push(url)
        const img = document.createElement('img')
        img.src = url
        if (el) el.appendChild(img)
        imgs.push(img)
      }
      if (disposed) return
      await Promise.all(
        imgs.map((img) => (img.complete ? null : new Promise((r) => ((img.onload = r), (img.onerror = r)))))
      )
      if (disposed || !el) return
      viewer = new Viewer(el, {
        inline: true,
        button: false,
        navbar: true,
        // this 是 viewer 实例:index/length 是实例属性,imageData 里没有
        title: function () {
          return `${this.index + 1} / ${this.length} 页`
        },
        toolbar: {
          zoomIn: true,
          zoomOut: true,
          oneToOne: true,
          reset: true,
          prev: true,
          play: false,
          next: true,
          rotateLeft: true,
          rotateRight: true,
          flipHorizontal: true,
          flipVertical: true
        },
        tooltip: true,
        movable: true,
        zoomable: true,
        rotatable: true,
        scalable: true,
        transition: false
      })
    } catch {
      error = '解码失败(可能是不支持的压缩格式),请下载查看'
    }
  }

  // 页数据转 PNG blob → objectURL,toBlob 异步不阻塞主线程
  function rgbaToObjectURL(w, h, rgba) {
    const c = document.createElement('canvas')
    c.width = w
    c.height = h
    c.getContext('2d').putImageData(new ImageData(new Uint8ClampedArray(rgba), w, h), 0, 0)
    return new Promise((resolve, reject) =>
      c.toBlob((b) => (b ? resolve(URL.createObjectURL(b)) : reject(new Error('toBlob failed'))), 'image/png')
    )
  }
</script>

<div class="viewer image-viewer">
  {#if error}
    <div class="hint error">
      <p>{error}</p>
      <a class="btn" href={fileUrl(path)} download>下载</a>
    </div>
  {:else if tooBig}
    <div class="hint">
      <p>{tooBig === 'size' ? '文件过大(>5MB),请下载查看' : '图片分辨率过高,无法在线预览,请下载查看'}</p>
      <a class="btn" href={fileUrl(path)} download>下载</a>
    </div>
  {:else}
    <div class="image-viewer-source" bind:this={source}></div>
  {/if}
</div>
