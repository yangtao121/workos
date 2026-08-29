// Command workos-web-fixture is the deterministic container fixture image
// payload: a static HTTP server with a fixed marker page and a /health
// endpoint, plus an argv-selected memory-hog mode used for the controlled
// OOM probe. It is built into a FROM scratch image by the opt-in fixture
// test; the runtime itself never builds images.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

const marker = "WORKOS-WEB-FIXTURE-OK"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hog" {
		hog()
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body data-fixture=\"" + marker + "\">fixture</body></html>"))
	})
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// hog allocates beyond the container's memory limit in bounded chunks and
// then sleeps, so the kernel OOM-killer terminates the cgroup deterministically.
func hog() {
	chunks := make([][]byte, 0, 64)
	for i := 0; i < 64; i++ {
		chunk := make([]byte, 8*1024*1024)
		for index := range chunk {
			chunk[index] = byte(index)
		}
		chunks = append(chunks, chunk)
		fmt.Fprintln(os.Stderr, "chunk", i)
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(10 * time.Minute)
}
