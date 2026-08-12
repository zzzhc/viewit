# viewit

Go 静态文件浏览器:在浏览器里直接查看文件(图片、视频、音频、源代码、PDF),无需下载。前端 Svelte 5,生产构建产物经 `go:embed` 嵌入单二进制。

## 生产模式

```bash
go build -o viewit .
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
