// Command serve 为网页运行时提供静态文件服务。
//
// 页面用的是 ES module + fetch 加载图集，必须经 http:// 访问（file:// 会被
// 浏览器的同源策略挡住），所以本地起一个零依赖的静态服务器是最省事的方式。
//
//	go run ./cmd/serve -addr 127.0.0.1:8098 -dir web
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8098", "监听地址")
	dir := flag.String("dir", "web", "静态资源根目录")
	flag.Parse()

	root, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		log.Fatalf("静态目录不可用：%s", root)
	}

	fs := http.FileServer(http.Dir(root))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 开发期禁用缓存，避免改了 JS 之后浏览器还拿旧的
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		fs.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("ISO-CITY '98 运行于 http://%s/  （根目录 %s）\n", *addr, root)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
