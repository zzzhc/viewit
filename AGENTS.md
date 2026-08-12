# Repository Guidelines

## 核心原则

- 始终使用中文思考和回复，项目内新增说明文档使用中文。
- 聚焦最终目标，主动完成实现与验证；除非因信息缺失、安全风险或不可逆操作受阻，否则无需反复确认。
- 用户负责提出需求，Agent 负责产品思考、实现和验收，不把验收工作推给用户。
- 简洁胜于巧妙：优先清晰的数据结构、稳定的接口、一致的模式和最小必要自动化，避免过度设计与冗余测试。
- 不生成无明确用途的中间文档、总结或一次性脚本；仅用于验证的文件在任务完成后删除。

## Project Overview

Go 静态文件浏览器:单二进制(`go:embed frontend/dist`)内置 Svelte 5 前端,纯标准库、零 Go 依赖。浏览器直接预览文件,无需下载。

## Architecture & Data Flow

- 入口 `main.go` → `newHandler(root, dev)` → 单 mux 四路由(`/api/list`、`/api/file`、index、SPA 回退)。路由与 JSON 结构见 `server.go`,不必在此重复。
- `resolve()` 是路径安全核心:字符串拒 `..`/绝对路径 → `EvalSymlinks` → 结果必须落在 root 下。纵深防御,勿削弱。
- 前端:hash 路由(`#/path`)手写,无路由库;`App.svelte` 经 `viewerFor(name, mime)` 分派 viewer。新增文件类型 = `viewers.js` 注册 + `.svelte` 组件 + `App.svelte` 分派,三处缺一不可。
- 开发模式:Vite :5173 代理 `/api` → `VITE_BACKEND || :8080`;后端 `-dev` 时仍服务嵌入前端,可独立用。

## Development Commands

README.md 已有完整命令(构建/运行/dev/测试),此处只列易错点:

```bash
cd frontend && npm install && npm run build   # 必须先于 go build
go vet ./... && go test ./...
```

**陷阱**:`frontend/dist` 仅 `index.html` 入库,克隆后不先 `npm run build` 则 `go:embed` 编译失败。

## Code Conventions & Common Patterns

- Go:纯标准库;handler 为 `*server` 方法;哨兵错误(`errOutsideRoot`)经 `mapResolveErr` 映射状态码;JSON 错误统一 `writeErr` → `{"error": msg}`。
- MIME 以内容嗅探(`sniffMime`)为准,扩展名仅供参考。
- 前端:纯逻辑放普通 JS 模块,UI 在 `.svelte`(`viewers.js`/`xmlTree.js`/`format.js` 均无 UI);`api.js` 是唯一 fetch 入口,非 2xx 抛带服务端消息的 Error;代码/XML 查看器 5MB 上限超限提示下载;UI 文案中文。
- 无 lint/format 配置、无 CI、无 Makefile——改动后自测靠 `go test`。

## Important Files

- `server.go` / `server_test.go`:后端全部逻辑与其唯一回归屏障
- `frontend/src/api.js` + `viewers.js`:前后端契约与 viewer 分派
- `frontend/vite.config.js`:代理目标,改端口时同步 `VITE_BACKEND`

## Runtime/Tooling Preferences

Go 1.26.5 零依赖;前端 Svelte 5 + Vite 8,npm 管理。交付物为 gitignore 的单二进制 `viewit`。

## Testing & QA

- 标准库 `testing` + `httptest`,黑盒测 `newHandler`,`t.TempDir()` 每测隔离,不起真实端口、不依赖前端构建。
- 辅助函数:`newTestHandler`/`writeFile`/`doGet`/`decodeList`/`assertStatus`。
- 14 测试覆盖:列表排序、路径穿越/符号链接逃逸拒绝、Range/206、MIME 嗅探、SPA 服务。前端无测试。
