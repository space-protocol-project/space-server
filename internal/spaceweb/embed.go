package spaceweb

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed assets/*
var assets embed.FS

func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "Метод недоступен", 405)
			return
		}
		file := strings.TrimPrefix(r.URL.Path, "/space/")
		if r.URL.Path == "/space" || r.URL.Path == "/space/" {
			file = "index.html"
		}
		types := map[string]string{"index.html": "text/html; charset=utf-8", "app.mjs": "text/javascript; charset=utf-8", "identity.mjs": "text/javascript; charset=utf-8", "styles.css": "text/css; charset=utf-8"}
		types["recovery.mjs"] = "text/javascript; charset=utf-8"
		types["pairing.mjs"] = "text/javascript; charset=utf-8"
		for _,name:=range []string{"recovery-qr.mjs","vendor/qrcode.mjs","vendor/jsqr.mjs"}{types[name]="text/javascript; charset=utf-8"}
		media, ok := types[file]
		if !ok {
			http.NotFound(w, r)
			return
		}
		content, err := assets.ReadFile("assets/" + file)
		if err != nil {
			http.Error(w, "Панель недоступна", 500)
			return
		}
		w.Header().Set("Content-Type", media)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == "GET" {
			_, _ = w.Write(content)
		}
	})
}
