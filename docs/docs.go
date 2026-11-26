package docs

import (
	_ "embed"
	"net/http"

	"github.com/go-chi/chi/v5"
	swaggerFiles "github.com/swaggo/files/v2"
)

//go:embed openapi.yaml
var specification []byte

//go:embed index.html
var swaggerPage []byte

func Mount(r chi.Router) {
	r.Get("/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Write(specification)
	})
	r.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusTemporaryRedirect)
	})
	r.Get("/swagger/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(swaggerPage)
	})
	r.Get("/swagger/index.html", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusTemporaryRedirect)
	})
	r.Handle("/swagger/*", http.StripPrefix("/swagger/", http.FileServer(http.FS(swaggerFiles.FS))))
}
