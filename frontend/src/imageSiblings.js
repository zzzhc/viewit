import { listImages } from './api.js'

// 目录 -> 图片文件名数组(目录排序序)的会话内缓存:同目录内连续切换
// 上一张/下一张时不重复拉列表。失败不缓存(下次重试);缓存条目在目录
// 内容变化后会过期(与 finder 索引一致的会话级一致性,刷新页面即失效)。
const cache = new Map()

export function imageSiblings(dir) {
  let p = cache.get(dir)
  if (!p) {
    // 缓存 promise 本身(并发调用共享同一请求);resolve 后不再覆盖,
    // 否则缓存会变成数组导致再次调用时 .then 不存在
    p = listImages(dir).catch((e) => {
      cache.delete(dir)
      throw e
    })
    cache.set(dir, p)
  }
  return p
}

// dirOf 返回 path 的父目录(root 下相对路径):"a/b.png" -> "a","a.png" -> ""。
export function dirOf(p) {
  const i = p.lastIndexOf('/')
  return i > 0 ? p.slice(0, i) : ''
}

// baseOf 返回 path 的文件名部分。
export function baseOf(p) {
  const i = p.lastIndexOf('/')
  return i >= 0 ? p.slice(i + 1) : p
}
