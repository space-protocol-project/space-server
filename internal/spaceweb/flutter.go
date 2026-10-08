package spaceweb

import (
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type flutterBuildHandler struct {
	http.Handler
	root *os.Root
}

func (h *flutterBuildHandler) Close() error { return h.root.Close() }

// Root ограничивает чтение каталогом сборки, включая переходы по symlink.
func FlutterHandler(directory string) (http.Handler, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("Каталог сборки Space Admin недоступен")
	}
	if _, err = root.Stat("index.html"); err != nil {
		root.Close()
		return nil, fmt.Errorf("В сборке Space Admin отсутствует index.html")
	}
	return &flutterBuildHandler{root: root, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Метод недоступен", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/space/flutter/")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		file, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; font-src 'self'; worker-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=()")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		media := mime.TypeByExtension(filepath.Ext(name))
		switch filepath.Ext(name) {
		case ".wasm":
			media = "application/wasm"
		case ".mjs", ".js":
			media = "text/javascript; charset=utf-8"
		case ".ttf":
			media = "font/ttf"
		}
		if media != "" {
			w.Header().Set("Content-Type", media)
		}
		http.ServeContent(w, r, name, time.Time{}, file)
	})}, nil
}
