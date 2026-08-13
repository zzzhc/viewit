<script>
  import { untrack } from 'svelte'
  import { fileUrl } from './api.js'
  import { imageSiblings, dirOf, baseOf } from './imageSiblings.js'
  import { isTifName } from './viewers.js'
  import UTIF from 'utif'
  import Viewer from 'viewerjs'
  import 'viewerjs/dist/viewer.css'

  let { path } = $props()

  let error = $state('')
  let source

  // TIFF 解码上限(与代码/XML 查看器 5MB 同思路):未压缩 RGBA 内存与解码
  // 耗时随像素线性增长,超限拒绝预览(约 120MB/页、240MB 合计)
  const MAX_PAGE_PIXELS = 30_000_000
  const MAX_TOTAL_PIXELS = 60_000_000
  // 1x1 透明 GIF:TIFF 解码前的占位图。源 img 不能带真实 src(浏览器会预
  // 下载),但 data-src 全空时 viewerjs 收集到空列表不会执行 build——
  // 占位图让它正常构建,解码完成后再整体替换列表。
  const PLACEHOLDER = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7'

  // tif/tiff 浏览器 <img> 不支持,经 UTIF.js 解码每页转 PNG blob,多页
  // 展开为多个列表项。返回 blob URL 数组;过大/无法解析抛错。
  async function tifPages(url) {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const buf = await res.arrayBuffer()
    const ifds = UTIF.decode(buf)
    if (!ifds || !ifds.length) throw new Error('no ifd')
    // width/height 是 decodeImage 之后才设置的属性,这里读原始 tag
    // (t256=ImageWidth, t257=ImageLength),超限的图不解码直接拒绝
    let total = 0
    for (const ifd of ifds) {
      const w = ifd['t256'] && ifd['t256'][0]
      const h = ifd['t257'] && ifd['t257'][0]
      const px = w && h ? w * h : 0
      total += px
      if (px > MAX_PAGE_PIXELS || total > MAX_TOTAL_PIXELS) throw new Error('too big')
    }
    const urls = []
    for (const ifd of ifds) {
      UTIF.decodeImage(buf, ifd)
      const rgba = UTIF.toRGBA8(ifd)
      const c = document.createElement('canvas')
      c.width = ifd.width
      c.height = ifd.height
      c.getContext('2d').putImageData(new ImageData(new Uint8ClampedArray(rgba), ifd.width, ifd.height), 0, 0)
      const blob = await new Promise((resolve, reject) =>
        c.toBlob((b) => (b ? resolve(b) : reject(new Error('toBlob failed'))), 'image/png')
      )
      urls.push(URL.createObjectURL(blob))
    }
    return urls
  }

  // 同目录切换 + TIFF 解码显示:viewerjs 的 prev/next 在图片列表上循环
  // 切换,列表 = 同目录全部图片(目录排序序,TIFF 多页展开为多个列表项)。
  // 源 img 只带 data-src 不带 src,浏览器不预下载;viewerjs 经 url 选项
  // 在 view() 时按需加载。TIFF 页是解码出的 PNG blob URL,随列表进 < >。
  $effect(() => {
    untrack(() => {
      error = ''
    })
    const p = path
    const el = source
    if (!el) return
    const dir = dirOf(p)
    const curName = baseOf(p)
    const isCurTif = isTifName(curName)
    let cancelled = false
    let viewer = null
    let items = [] // 扁平列表 { name, url, page?, total? };TIFF 多页展开
    let suppressHash = false // rebuild/定位期间的 view 事件不写 hash
    const blobUrls = []

    const urlOf = (name) => fileUrl(dir ? dir + '/' + name : name)

    const imgEl = (name, url) => {
      const img = document.createElement('img')
      if (url) img.setAttribute('data-src', url)
      img.alt = name
      return img
    }

    // 重建列表:重扫源容器后 update(),随后定位到 target(可空)。
    // 空 url 的占位项(未解码 TIFF)不会被收集,列表只含可显示项。
    // update() 因列表变化会自动 view(跳 index),这是程序化定位不是用户
    // 切换,期间抑制 onView 的 hash 写入,避免 URL 被改到别的文件。
    const rebuild = (list, target) => {
      // items 只保留可显示项(空 url 的占位 TIFF 不会被 viewerjs 收集),
      // 与 viewerjs 的 index 一一对应;占位项仍渲染在源容器里保序
      items = list.filter((it) => it.url)
      el.innerHTML = ''
      for (const it of list) el.appendChild(imgEl(it.name, it.url))
      suppressHash = true
      viewer.update()
      if (target) {
        const wi = items.findIndex((it) => it.name === target)
        if (wi >= 0 && wi !== viewer.index) viewer.view(Math.max(0, wi))
      }
      suppressHash = false
      setNav(items.length > 1)
    }

    const setNav = (show) => {
      const root = viewer.viewer
      if (!root) return
      for (const sel of ['.viewer-prev', '.viewer-next']) {
        const btn = root.querySelector(sel)
        if (btn) btn.classList.toggle('viewer-hide', !show)
      }
    }

    // 初始只放当前图片:TIFF 解码前用透明占位,避免 <img> 加载 tif 字节
    el.appendChild(imgEl(curName, isCurTif ? PLACEHOLDER : urlOf(curName)))

    viewer = new Viewer(el, {
      inline: true,
      button: false,
      navbar: false,
      // 多页 TIFF 的列表项显示 "名称 · 页 i/n";列表未就绪(初始单图,
      // viewed 只触发一次)时回退到 img 的 alt(文件名)
      title: function () {
        const it = items[this.index]
        if (it) return it.page ? `${it.name} · 页 ${it.page}/${it.total}` : it.name
        const img = this.items[this.index]?.querySelector('img')
        return img ? img.alt : ''
      },
      // 图片地址取自 data-src(源 img 无 src 不触发预下载)
      url: (img) => img.getAttribute('data-src'),
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

    // 切换图片时同步 URL(hash 路由):replaceState 不触发 hashchange,
    // App 不会重载 viewer,刷新/分享打开的仍是当前文件。TIFF 页间切换
    // 文件名不变,hash 幂等。
    // viewerjs 的事件在源容器上以裸名派发(CustomEvent 'view'),实例无 on 方法。
    const onView = (e) => {
      const it = items[e.detail.index]
      if (!it || suppressHash) return
      // 当前文件无法预览(错误 overlay)时,切换显示别的文件即恢复正常
      if (error && it.name !== curName) error = ''
      const target = dir ? dir + '/' + it.name : it.name
      const h = '#/' + target.split('/').map(encodeURIComponent).join('/')
      if (h !== location.hash) history.replaceState(null, '', h)
    }
    el.addEventListener('view', onView)

    // viewerjs inline 模式 build 是异步的(setTimeout 0),工具栏等 DOM
    // 在 ready 事件时才就绪:同目录列表初始化与按钮显隐都放这里。
    // TIFF 逐个解码(当前文件优先),每完成一个并入列表;失败项跳过,
    // 当前文件解码失败则显示下载提示。
    const onReady = () => {
      if (cancelled) return
      imageSiblings(dir)
        .then(async (names) => {
          if (cancelled) return
          const slots = names.map((n) =>
            isTifName(n) ? { name: n, url: '' } : { name: n, url: urlOf(n) }
          )
          rebuild(slots, isCurTif ? null : curName)
          const tifs = names.filter(isTifName)
          const ordered = isCurTif
            ? [curName, ...tifs.filter((n) => n !== curName)]
            : tifs
          for (const n of ordered) {
            let pages
            try {
              pages = await tifPages(urlOf(n))
            } catch {
              if (n === curName) {
                error = '无法预览此 TIFF(过大或格式不支持),请下载查看'
              }
              continue
            }
            if (cancelled) return
            const idx = slots.findIndex((it) => it.name === n)
            if (idx < 0) continue
            slots.splice(
              idx,
              1,
              ...pages.map((u, i) => ({ name: n, url: u, page: i + 1, total: pages.length }))
            )
            for (const u of pages) blobUrls.push(u)
            const prevShown = items[viewer.index]?.name
            rebuild(slots, n === curName ? curName : prevShown)
          }
        })
        .catch(() => {
          // 目录列表拉取失败:保持单图显示,不提供切换
          if (!cancelled) setNav(false)
        })
    }
    el.addEventListener('ready', onReady)

    return () => {
      cancelled = true
      el.removeEventListener('view', onView)
      el.removeEventListener('ready', onReady)
      if (viewer) viewer.destroy()
      for (const u of blobUrls) URL.revokeObjectURL(u)
    }
  })
</script>

<div class="viewer image-viewer">
  <div class="image-viewer-source" bind:this={source}></div>
  {#if error}
    <!-- 错误以 overlay 呈现:source 保持渲染,effect 不因 error 重跑,
         避免"解码失败 → 卸载 → 重跑清 error → 再解码"的循环 -->
    <div class="image-viewer-error">
      <p>{error}</p>
      <a class="btn" href={fileUrl(path)} download>下载</a>
    </div>
  {/if}
</div>
