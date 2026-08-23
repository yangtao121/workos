package gateway

import (
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type Handler struct {
	config config.Config
	proxy  *httputil.ReverseProxy
	logger *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) (*Handler, error) {
	target, err := url.Parse(cfg.Services.Core)
	if err != nil {
		return nil, fmt.Errorf("parse core URL: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(request *http.Request) {
		original(request)
		request.Header.Del(identity.UserHeader)
		request.Header.Del(identity.DeviceHeader)
		request.Header.Set(identity.UserHeader, cfg.Auth.OwnerID)
		request.Header.Set(identity.DeviceHeader, cfg.Auth.DeviceID)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		logger.Error("core proxy failed", "error", err)
		http.Error(w, "workos core unavailable", http.StatusServiceUnavailable)
	}
	return &Handler{config: cfg, proxy: proxy, logger: logger}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/workos.") {
		if !h.config.Auth.DevBypass {
			http.Error(w, "device session required", http.StatusUnauthorized)
			return
		}
		h.proxy.ServeHTTP(w, r)
		return
	}
	h.serveStatic(w, r)
}

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if path == "." {
		path = "index.html"
	}
	root := os.DirFS(h.config.HTTP.StaticDir)
	data, err := fs.ReadFile(root, path)
	if err != nil {
		path = "index.html"
		data, err = fs.ReadFile(root, "index.html")
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("<!doctype html><title>WorkOS</title><h1>Desktop build unavailable</h1><p>Run make web-build.</p>"))
		return
	}
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	_, _ = w.Write(data)
}
