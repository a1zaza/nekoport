package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var files embed.FS

func Check() error {
	b, e := files.ReadFile("dist/index.html")
	if e != nil || len(b) < 100 {
		return fmt.Errorf("缺少前端发布资源，请先在 web 执行 npm ci --ignore-scripts 和 npm run build，再编译 Go")
	}
	return nil
}
func Handler() http.Handler {
	root, _ := fs.Sub(files, "dist")
	server := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "不支持的请求方法", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, e := fs.Stat(root, name); e != nil {
			if r.URL.Path == "/mail" || r.URL.Path == "/settings" {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			} else {
				http.NotFound(w, r)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		server.ServeHTTP(w, r)
	})
}
