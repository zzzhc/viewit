# viewit

Go 静态文件浏览器:在浏览器里直接查看文件(图片、视频、音频、源代码、PDF),无需下载。前端 Svelte 5,生产构建产物经 `go:embed` 嵌入单二进制。

## 文件查找

`Ctrl+P` 或快速连按三次 `Shift` 唤起查找面板,输入关键字即时模糊匹配(支持中文等 Unicode 文件名,命中字符高亮)。`↑`/`↓` 选择,`Enter` 打开,`Esc` 关闭。

在子目录(或文件预览页)唤起面板时,只在当前目录内查找并显示该目录的规模;回到根目录则全库查找。索引在首次连接时惰性构建,构建期间返回部分结果并自动重查;`.git` 目录不入索引。

匹配在服务端完成(`sahilm/fuzzy` 分块扫描,结果上限 200 条,内存占用有界);前端经 WebSocket(`/api/ws`)通信。索引在首次连接时惰性构建,构建期间返回部分结果并自动重查;`.git` 目录不入索引。

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
cd frontend && npm install && npm run build   # 输出到 frontend/dist,下次 go build 时嵌入
```

## 测试

```bash
go vet ./... && go test ./...
```
