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
	"os/exec"
	"runtime"
	"time"
)

const marker = "WORKOS-WEB-FIXTURE-OK"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hog":
			hog()
			return
		case "pids":
			hitPIDsLimit()
			return
		case "child":
			time.Sleep(10 * time.Minute)
			return
		}
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

// hitPIDsLimit creates child tasks until the kernel rejects one at pids.max,
// then stays alive so the fixture can read the cumulative pids.events `max`
// counter from the same cgroup.
func hitPIDsLimit() {
	children := make([]*os.Process, 0, 64)
	defer func() {
		for _, child := range children {
			_ = child.Kill()
		}
	}()
	for len(children) < 64 {
		command := exec.Command(os.Args[0], "child")
		if err := command.Start(); err != nil {
			break
		}
		children = append(children, command.Process)
	}
	time.Sleep(10 * time.Minute)
}

// hog allocates beyond the container's memory limit in bounded chunks and
// then sleeps, so the kernel OOM-killer terminates the cgroup deterministically.
func hog() {
	// Give the host fixture time to resolve this process's cgroup before the
	// first allocation can trigger the kill.
	time.Sleep(2 * time.Second)
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
