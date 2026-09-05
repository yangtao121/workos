// Command browser-worker-fixture is the versioned remote-browser worker
// stand-in (structure §10 Remote Browser). It serves the deterministic
// bounded browser UI the Browser Surface renders — tabs, address bar, and a
// content pane driven by query parameters — over loopback HTTP with zero
// external dependencies and no real browser process.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

const pageTemplate = `<!doctype html>
<html><head><meta charset="utf-8"><title>Browser Fixture</title></head>
<body id="browser-fixture">
<nav id="tabs" aria-label="Browser tabs">%s</nav>
<div id="address" aria-label="Address bar">%s</div>
<main id="content" aria-label="Browser content">%s</main>
</body></html>`

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;")
	return r.Replace(s)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		tab := strings.TrimSpace(r.URL.Query().Get("tab"))
		if tab == "" {
			tab = "start"
		}
		tabs := []string{"start", "docs", "review"}
		var nav strings.Builder
		for _, name := range tabs {
			cls := "tab"
			if name == tab {
				cls += " active"
			}
			fmt.Fprintf(&nav, `<span class="%s" data-tab="%s">%s</span> `, cls, name, esc(name))
		}
		content := map[string]string{
			"start":  "Browser fixture start page",
			"docs":   "Fixture documentation pane",
			"review": "Fixture review pane",
		}
		page := fmt.Sprintf(pageTemplate, nav.String(), esc(tab), esc(content[tab]))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	server := &http.Server{Addr: "127.0.0.1:8080", Handler: mux}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
