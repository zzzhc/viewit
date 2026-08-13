// stream.js — 大文本文件按需流式读取客户端(逻辑层,UI 在 StreamViewer.svelte)
// 协议:连接 /api/stream,发送 {"type":"open","path":"..."} 开始,
// {"type":"more","bytes":N} 拉取更多字节;接收
// {"type":"meta","name","mime"} / {"type":"data","offset","b64"} /
// {"type":"end","size"} / {"type":"error","error"}。
// 普通文件与 .gz 文件走同一通道:.gz 由服务端透明解压,客户端无感知。
// 客户端用单个 TextDecoder(stream 模式)跨块解码,UTF-8 多字节字符
// 被块边界截断也不会出错。

export class StreamReader {
  constructor({ path, onMeta, onData, onEnd, onError }) {
    this.path = path
    this.onMeta = onMeta
    this.onData = onData // (text, byteLength)
    this.onEnd = onEnd // (totalBytes)
    this.onError = onError // (message)
    this.ws = null
    this.decoder = new TextDecoder('utf-8')
    this.eof = false
  }

  connect() {
    if (this.ws) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/api/stream`)
    this.ws = ws
    ws.onopen = () => {
      ws.send(JSON.stringify({ type: 'open', path: this.path }))
    }
    ws.onmessage = (ev) => {
      let msg
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!msg || !msg.type) return
      switch (msg.type) {
        case 'meta':
          this.onMeta(msg)
          break
        case 'data': {
          const bytes = Uint8Array.from(atob(msg.b64), (c) => c.charCodeAt(0))
          const text = this.decoder.decode(bytes, { stream: true })
          if (text) this.onData(text, bytes.byteLength)
          break
        }
        case 'end': {
          this.eof = true
          const tail = this.decoder.decode()
          if (tail) this.onData(tail, 0)
          this.onEnd(msg.size || 0)
          break
        }
        case 'error':
          this.onError(msg.error || '未知错误')
          break
      }
    }
    ws.onclose = () => {
      this.ws = null
    }
  }

  request(bytes) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN && !this.eof) {
      this.ws.send(JSON.stringify({ type: 'more', bytes }))
    }
  }

  close() {
    if (this.ws) {
      this.ws.onclose = null
      this.ws.close()
      this.ws = null
    }
  }
}
