# viewit

> 浏览器看遍服务器文件。

在浏览器里直接查看服务器上的文件:tar/zip 不解压直览、大 JSONL/日志流式翻阅、图片视频即点即看。单文件、零依赖,一条命令跑起来。

图片、视频、音频、源代码、Markdown、PDF……打开浏览器即可预览;内置模糊搜索,海量文件也能秒级定位。

## 快速开始

```bash
./build.sh                # 一键构建(自动安装前端依赖)
./viewit --version        # 查看版本(取自 git tag,如 v1.0.0)
./viewit -root <目录>     # 监听 :8080,浏览器打开 http://localhost:8080
```

就绪。没有 Node、没有环境配置、没有任何外部依赖——只有一个静态二进制,拷到任何 Linux 机器就能跑,也方便塞进 Docker 或内网服务器。加上 `-addr 0.0.0.0:8080` 即可让同网络设备直接访问。

## 为什么选 viewit?

- **无需下载,即开即看** —— 浏览文件不再先下载再看:图片、视频、音频、PDF、源代码、Markdown 全在浏览器内预览,想保存时再下载。
- **Markdown 双栏预览** —— 左侧源码、右侧实时渲染,支持 LaTeX 公式(KaTeX)、Mermaid 图表、代码高亮、GFM 表格与任务列表;相对链接站内跳转,写作、分享、评审文档体验完整。
- **毫秒级文件查找** —— `Ctrl+P` 或连按三次 `Shift` 唤起搜索,输入即出结果,支持中文等 Unicode 文件名,命中字符高亮;子目录内自动缩小范围,结果上限 200 条,内存占用有界。
- **安全、私有、只读** —— 严格路径校验与符号链接防护,任何请求都无法越出根目录;纯浏览不写文件,数据只留在你自己的机器上。
- **压缩包直接浏览,无需解压** —— zip/tar 在页面里展开成虚拟目录树,内层文件(Markdown、代码、图片)照常预览;tar 索引有进程级缓存,反复浏览不重扫,大成员按需流式读取,不占磁盘。
- **超大文件、超大目录不卡** —— 超过 5MB 的文本/代码/JSONL 自动切换流式查看器,按需拉取、窗口化渲染,内存恒定不随文件大小增长,几十 GB 的日志也能流畅翻页;超大目录虚拟滚动,首屏只加载一页。
- **同目录图片连看** —— 图片查看器(含 TIFF)的上一张/下一张在同目录图片间循环切换,浏览图册无需逐个返回目录;TIFF 前端解码为 PNG 显示,多页 TIFF 逐页展开,标题显示页码。
- **轻量快速** —— 静态资源在构建期预压缩、缓存策略优化、无运行时压缩,低配服务器也能长期挂机。

## 支持的文件类型

| 类型 | 说明 |
|---|---|
| 图片 / 视频 / 音频 | 浏览器原生播放与预览;TIFF(含多页)由 UTIF.js 解码成 PNG 预览 |
| 源代码 / 文本 | 语法高亮,含 Dockerfile、go.mod 等特殊文件;超 5MB 提示下载 |
| Markdown | 双栏实时渲染:LaTeX、Mermaid、GFM、原始 HTML |
| PDF / XML / JSONL | 直接查看 |
| tar / zip | 不解压直接浏览内部目录,内层文件照常预览 |

自动识别不准?点击预览页顶部的类型徽标可手动指定(如把无扩展名的 Markdown 按 Markdown 打开、让未知语言的脚本按指定语言高亮),仅对当前文件生效,切换文件后自动恢复。

## 面向开发者

```bash
# 开发模式(前端 HMR)
go run . -dev -root <目录> -addr :8080    # 后端可独立使用
cd frontend && npm run dev                # Vite HMR,代理 /api 到 :8080

# 构建与测试
./build.sh      # 前端构建 + 预 gzip + 静态裁剪编译,产物为单文件 viewit
# 手工构建:npm run build 后必须先 go generate(把 frontend/dist 预 gzip 成
# frontend/dist.gz,go:embed 嵌入的是 dist.gz),再 go build;漏跑 go generate
# 会嵌入上一次的旧前端。
cd frontend && npm run build && cd .. && go generate ./... && go build
go vet ./... && go test ./...
```

- 发布:推送 `v*` tag(如 `git push origin v1.0.0`)后 GitHub Actions 自动构建 linux amd64/arm64 二进制并创建 Release,版本号取 tag 名,`viewit --version` 可查。
- 单二进制:前端经 `go:embed` 内嵌,构建期预 gzip,运行时以 `Content-Encoding: gzip` 原样下发,不耗请求期 CPU。
- 访问日志输出到 stderr,按前缀分三类可 grep:`[access]` 请求级、`[ws]` 消息级、`[slow]` 慢操作(阈值可用 `-slow` 调整)。

## 许可证

[MIT](LICENSE)。
