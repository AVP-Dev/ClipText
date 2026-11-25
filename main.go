package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"cliptext/internal/server"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	defaultAddr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		defaultAddr = ":" + p
	}
	addr := flag.String("addr", defaultAddr, "HTTP listen address")
	openBrowser := flag.Bool("open", true, "Open browser automatically on start")
	flag.Parse()

	url := "http://localhost" + *addr
	if (*addr)[0] != ':' {
		url = "http://" + *addr
	}

	fmt.Printf("ClipText running at %s\n", url)

	if *openBrowser && runtime.GOOS == "darwin" {
		go func() {
			_ = exec.Command("open", url).Start()
		}()
	}

	if err := http.ListenAndServe(*addr, server.New(indexHTML)); err != nil {
		log.Fatal(err)
	}
}
