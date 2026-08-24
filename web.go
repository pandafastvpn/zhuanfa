package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var webFS embed.FS

// staticFS 是 web/static/ 子文件系统，用于 /static/ 路由。
var staticFS = func() fs.FS {
	s, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return s
}()

func (a *App) handlePage(w http.ResponseWriter, r *http.Request, name string) {
	b, err := webFS.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch p {
	case "/":
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	case "/login":
		a.handlePage(w, r, "login.html")
		return
	case "/register":
		a.handlePage(w, r, "register.html")
		return
	case "/admin":
		a.handlePage(w, r, "admin.html")
		return
	case "/user":
		a.handlePage(w, r, "user.html")
		return
	}
	if strings.HasPrefix(p, "/static/") {
		http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))).ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(p, "/api/") {
		a.routeAPI(w, r)
		return
	}
	http.NotFound(w, r)
}