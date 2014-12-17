package admin

import (
	"bytes"
	"net/http"
	"time"

	"gateway/config"
	aphttp "gateway/http"

	"github.com/gorilla/mux"
)

func subrouter(router *mux.Router, config config.ProxyAdmin) *mux.Router {
	adminRoute := router.NewRoute()
	if config.Host != "" {
		adminRoute = adminRoute.Host(config.Host)
	}
	if config.PathPrefix != "" {
		adminRoute = adminRoute.PathPrefix(config.PathPrefix)
	}
	return adminRoute.Subrouter()
}

// AddRoutes adds the admin routes to the specified router.
func AddRoutes(router *mux.Router, conf config.ProxyAdmin) {
	var admin aphttp.Router
	admin = aphttp.NewAccessLoggingRouter(config.Admin, subrouter(router, conf))
	admin = aphttp.NewHTTPBasicRouter(conf.Username, conf.Password, conf.Realm, admin)
	admin.Handle("/{path:.*}", http.HandlerFunc(adminStaticFileHandler))
}

func adminStaticFileHandler(w http.ResponseWriter, r *http.Request) {
	path := mux.Vars(r)["path"]
	if path == "" {
		path = "index.html"
	}
	serveFile(w, r, path)
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	data, err := Asset(path)
	if err != nil || len(data) == 0 {
		http.NotFound(w, r)
		return
	}

	content := bytes.NewReader(data)
	http.ServeContent(w, r, path, time.Time{}, content)
}