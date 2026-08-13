package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

// version 由构建时注入:build.sh 经 git describe 取最近 tag,以 -ldflags "-X main.version=..." 覆盖。
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	addr := flag.String("addr", ":8080", "listen address")
	root := flag.String("root", ".", "directory to serve")
	dev := flag.Bool("dev", false, "dev mode: API only (frontend served by Vite on :5173)")
	slow := flag.Duration("slow", time.Second, "log operations slower than this (e.g. 500ms; 0 logs all)")
	flag.Parse()
	if *showVersion {
		fmt.Printf("viewit %s\n", version)
		return
	}
	slowThreshold = *slow

	h, err := newHandler(*root, *dev)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("viewit %s serving %s on %s", version, *root, *addr)
	if *dev {
		log.Printf("dev mode: last-built frontend served here; Vite HMR at http://localhost:5173 (cd frontend && npm run dev)")
	}
	log.Fatal(http.ListenAndServe(*addr, h))
}
