package greenfield

import (
	"context"
	"encoding/json"
	"flag"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

func TestMain(m *testing.M) {
	if os.Getenv("WORKOS_GREENFIELD_TEST_PROXY") == "1" {
		serveTestProxy()
		return
	}
	if os.Getenv("WORKOS_GREENFIELD_TEST_APP") == "1" {
		path := os.Args[len(os.Args)-1]
		if err := os.WriteFile(path, []byte("v3-p0-marker\n"), 0o600); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		return
	}
	os.Exit(m.Run())
}

func serveTestProxy() {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	port := fs.String("bind-port", "0", "")
	apps := fs.String("applications", "", "")
	_ = fs.String("bind-ip", "", "")
	_ = fs.String("allow-origin", "", "")
	_ = fs.String("base-url", "", "")
	_ = fs.String("encoder", "", "")
	_ = fs.Parse(os.Args[1:])
	mux := http.NewServeMux()
	mux.HandleFunc("/code", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-compositor-session-id") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		raw, err := os.ReadFile(*apps)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var spec map[string]struct {
			Executable string   `json:"executable"`
			Args       []string `json:"args"`
		}
		if err := json.Unmarshal(raw, &spec); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		app := spec["/code"]
		cmd := exec.Command(app.Executable, app.Args...)
		env := []string{"WORKOS_GREENFIELD_TEST_APP=1"}
		for _, item := range os.Environ() {
			if strings.HasPrefix(item, "WORKOS_GREENFIELD_TEST_PROXY=") || strings.HasPrefix(item, "WORKOS_GREENFIELD_TEST_APP=") {
				continue
			}
			env = append(env, item)
		}
		cmd.Env = env
		if err := cmd.Start(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"pid": strconv.Itoa(cmd.Process.Pid), "key": "test-key"})
	})
	listener, err := net.Listen("tcp", "127.0.0.1:"+*port)
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("Listening on " + listener.Addr().String() + "\n")
	_ = http.Serve(listener, mux)
}

func TestUnavailableWithoutBinaries(t *testing.T) {
	engine := New("", "", "")
	if err := engine.Available(context.Background()); err == nil {
		t.Fatal("expected unavailable")
	}
}

func TestDetachKeepsAppAndStopEndsIt(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "note.txt")
	t.Setenv("WORKOS_GREENFIELD_TEST_PROXY", "1")
	t.Setenv("WORKOS_GREENFIELD_TEST_APP", "1")
	engine := New(os.Args[0], os.Args[0], dir)
	engine.AppArgs = []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	launched, err := engine.Launch(ctx, 1440, 900, marker)
	if err != nil {
		t.Fatal(err)
	}
	view := launched.(*display)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(marker); err == nil && string(raw) == "v3-p0-marker\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "v3-p0-marker\n" {
		t.Fatalf("file = %q err=%v", raw, err)
	}
	if _, err := launched.Connect(ctx, "v=0"); err != domain.ErrWrongEngine {
		t.Fatalf("connect = %v", err)
	}
	launched.Detach()
	if launched.Exited() {
		t.Fatal("detach stopped the app")
	}
	if _, err := view.ReadClipboard(); err != domain.ErrClipboardDisconnected {
		t.Fatalf("clipboard = %v", err)
	}
	view.AttachClipboard()
	if err := view.WriteClipboard(string(make([]byte, domain.MaxClipboardBytes+1))); err != domain.ErrClipboardTooLarge {
		t.Fatalf("oversize = %v", err)
	}
	if err := view.WriteClipboard("中文\n\temoji 😀"); err != nil {
		t.Fatal(err)
	}
	got, err := view.ReadClipboard()
	if err != nil || got != "中文\n\temoji 😀" {
		t.Fatalf("clipboard read %q %v", got, err)
	}
	launched.Stop()
	if !launched.Exited() {
		t.Fatal("stop left the app running")
	}
	if _, err := view.ReadClipboard(); err != domain.ErrClipboardDisconnected {
		t.Fatalf("clipboard after stop = %v", err)
	}
}
