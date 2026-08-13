package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	root := flag.String("root", ".", "directory to serve")
	dev := flag.Bool("dev", false, "dev mode: API only (frontend served by Vite on :5173)")
	slow := flag.Duration("slow", time.Second, "log operations slower than this (e.g. 500ms; 0 logs all)")
	flag.Parse()
	slowThreshold = *slow

	h, err := newHandler(*root, *dev)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("viewit serving %s on %s", *root, *addr)
	if *dev {
		log.Printf("dev mode: last-built frontend served here; Vite HMR at http://localhost:5173 (cd frontend && npm run dev)")
	}
	log.Fatal(http.ListenAndServe(*addr, h))
}
