# viewit

Go 静态文件浏览器:在浏览器里直接查看文件(图片、视频、音频、源代码、Markdown、PDF),无需下载。前端 Svelte 5,生产构建产物经 `go:embed` 嵌入单二进制。

## Markdown 预览

`.md`/`.markdown` 文件分左右两栏:左侧 Markdown 源码(语法高亮),右侧渲染预览,工具栏可分别折叠任一侧。支持:

- **LaTeX**:`$...$` 行内公式、`$$...$$` 块级公式(KaTeX)
- **Mermaid**:```mermaid 代码块渲染为图表(流程图、时序图、甘特图等)
- **代码高亮**:围栏代码块按语言高亮(highlight.js),与源码查看器共用
- **原始 HTML**:表格、行内样式等原样渲染
- **GFM**:表格、任务列表、删除线、自动链接
- **相对引用**:相对链接在站内导航(点击跳转其他文件),相对图片直接预览;页内锚点 `#标题` 可跳转

## 文件查找

`Ctrl+P` 或快速连按三次 `Shift` 唤起查找面板,输入关键字即时模糊匹配(支持中文等 Unicode 文件名,命中字符高亮)。`↑`/`↓` 选择,`Enter` 打开,`Esc` 关闭。

在子目录(或文件预览页)唤起面板时,只在当前目录内查找并显示该目录的规模;回到根目录则全库查找。索引在首次连接时惰性构建,构建期间返回部分结果并自动重查;`.git` 目录不入索引。

匹配在服务端完成(`sahilm/fuzzy` 分块扫描,结果上限 200 条,内存占用有界);前端经 WebSocket(`/api/ws`)通信。索引在首次连接时惰性构建,构建期间返回部分结果并自动重查;`.git` 目录不入索引。

## 手动指定文件类型

文件预览页头部显示当前查看类型(如"代码/文本 · 自动",自动 = 按内容嗅探 + 扩展名分派)。自动识别不对时(如无扩展名的 Markdown 被当纯文本、未知语言的脚本想按某语言高亮、文本文件想直接下载),点击该徽标弹出类型选择框:

- 弹窗列出全部受支持的文件类型,按图片/视频/音频/PDF/XML/Markdown/HTML/JSONL/代码文本分组(含 Dockerfile、`.bashrc`、`go.mod` 等特殊文件名);
- 顶部搜索框按名称、语言、分组即时筛选,快速定位;
- 选中后立即以该类型打开:代码类强制按所选语言高亮,底部"下载"项强制走下载页;
- 手动指定仅对当前文件生效,切换文件后恢复自动;弹窗内可一键"恢复自动"。

## 生产模式

```bash
go build -o viewit .   # 依赖 sahilm/fuzzy、gorilla/websocket,go mod tidy 已就绪
./viewit -root <目录> -addr :8080
```

打开 `http://localhost:8080`。前端已内嵌,单二进制分发,无任何外部依赖。

## 开发模式(前端 HMR)

后端与 Vite 二选一即可访问(dev 模式下后端也内嵌了最后一次构建的前端,`curl localhost:8081` 直接可用):

```bash
go run . -dev -root <目录> -addr :8080   # 直接打开 http://localhost:8080
cd frontend && npm run dev               # HMR 模式:打开 http://localhost:5173
```

Vite 的 `/api` 代理默认指向 `:8080`,后端端口不同时用环境变量覆盖:`VITE_BACKEND=http://localhost:8081 npm run dev`。

## 前端构建

```bash
cd frontend && npm install && npm run build   # 输出到 frontend/dist
go generate .                                 # 预 gzip 成 frontend/dist.gz(BestCompression)
go build -o viewit .                          # 嵌入压缩后的 frontend/dist.gz
```

`frontend/dist` 里的可压缩资源(JS/CSS/HTML/SVG/JSON 等)在嵌入前被 gzip 成
`<name>.gz`,已压缩格式(字体、图片)原样保留;运行时直接以 `Content-Encoding:
gzip` 原样下发压缩字节,不再运行时压缩,二进制更小且不耗请求期 CPU。

## 测试

```bash
go vet ./... && go test ./...
```

## 访问日志

日志输出到 stderr(重定向 `2>viewit.log` 保存),分两类,均可按前缀 grep:

- **`[access]`** — 每个 HTTP 请求一行:`远程地址 方法 URI 状态码 响应字节数 耗时`。覆盖全部路由(含 404/405);WebSocket(`/api/ws`、`/api/stream`)的耗时是整个会话时长,前端挂着不关的连接会暴露为长耗时。
- **`[ws]`** — WebSocket 消息级日志:`/api/ws` 每条查询一行(`q` 关键字、`scope` 查找范围、`scopeCount` 范围内条目数、`matched` 命中数、耗时);`/api/stream` 每次打开一行(`stream-open`,含内容类型;打开失败记错误原因),读到结尾一行(`stream-end`,含累计字节与 open 到 end 的总耗时),读取中途出错一行(`stream-error`)。
- **`[slow]`** — 耗时操作:tar 全量扫描(`scan-tar`,含条目数与失败原因)、大 zip 打开/大成员首次解压(`open-zip`/`extract-zip`)、查找索引构建(`find-walk`)、超过阈值的模糊查询(`find-query`)、目录打包下载(`zip-download`)、大 tar 磁盘索引解码(`tar-index-load`)。扫描类重操作无条件记录,其余超阈值才记(查询超阈值时与对应的 `[ws] find` 行同时出现)。

慢日志阈值默认 1s,用 `-slow` 调整(如 `-slow 500ms` 抓更细的慢查询,`-slow 0` 记录全部):

```bash
./viewit -root <目录> -slow 500ms 2>viewit.log
```

## HTTP 细节

- **内容编码协商**:API 与文件下载按 `Accept-Encoding`(含 q 值)选择编码,平局时优先压缩比更好的 brotli(`br`),其次 gzip;已压缩格式(图片、视频、压缩包)原样传输。可压缩响应一律带 `Vary: Accept-Encoding` 供缓存区分。前端静态资源在构建期已 gzip,固定以 `Content-Encoding: gzip` 下发(不参与 br 协商)。
- **缓存策略**:`assets/` 下 Vite 内容哈希资源 `public, max-age=31536000, immutable`;`index.html` 与 SPA 回退 `no-cache`;API 响应 `no-store`。
