package main

import (
	"competition2026/product/platform/internal/aimock"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "simulation endpoint")
	flag.Parse()
	s := http.Server{Addr: *listen, Handler: aimock.Server{}, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(s.ListenAndServe())
}
