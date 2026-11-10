package http

import (
	"net/http"
	"os"
)

// ---------------------------------------------------------------- static files

// static serves the admin web. Its files change with each release, so the
// browser revalidates them instead of caching them for long.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// uploadsHandler serves product images. Every file gets a new random name, so
// it can be cached for a year; directory listings are refused.
func uploadsHandler(dir string) http.Handler {
	files := http.FileServer(noListing{http.Dir(dir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

type noListing struct{ fs http.FileSystem }

func (n noListing) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}
