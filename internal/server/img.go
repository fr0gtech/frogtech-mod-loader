package server

import (
	"net/http"

	"github.com/fr0gtech/frogtech-mod-loader/internal/app"
)

func handleImg(a *app.App, w http.ResponseWriter, r *http.Request) {
	b, err := a.CachedImage(r.Context(), r.URL.Query().Get("u"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(b)
}
