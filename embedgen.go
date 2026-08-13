//go:build ignore

// embedgen 把 frontend/dist 预压缩成 frontend/dist.gz 供 go:embed 嵌入:
// 可压缩资源(JS/CSS/HTML/SVG/JSON 等)gzip 成 <name>.gz,已压缩格式(字体、
// 图片)原样复制。这样嵌入二进制的就是压缩后的字节,运行时直接以
// Content-Encoding: gzip 原样下发,无需再压缩,体积更小且不耗运行时 CPU。
//
// 用法:go generate(见 embed.go)或 go run embedgen.go。
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// compressibleExt 是可压缩扩展名清单,与运行时 compressible() 的 MIME 语义
// 对齐(构建期只能按扩展名判断,无法嗅探内容)。
var compressibleExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".html": true, ".htm": true,
	".svg": true, ".json": true, ".xml": true, ".txt": true, ".md": true,
	".map": true, ".wasm": true,
}

const (
	srcDir = "frontend/dist"
	dstDir = "frontend/dist.gz"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "embedgen:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dstDir, rel)
		if compressibleExt[strings.ToLower(filepath.Ext(rel))] {
			return gzipFile(path, out+".gz")
		}
		return copyFile(path, out)
	})
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	// BestCompression 最大化压缩比,换取构建期多花一点时间(嵌入体积优先)。
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
