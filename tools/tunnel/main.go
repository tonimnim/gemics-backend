// Command tunnel routes one public tunnel URL to the local stack, so a phone
// can use the API, the website, the staff dashboard and screenshot uploads
// through a single ngrok domain:
//
//	/v1/..., /healthz, /readyz  -> the API
//	/admin/...                  -> the dashboard build (a single-page app)
//	/<bucket>/...               -> MinIO, for presigned uploads and downloads
//	everything else             -> the website
//
// The Host header is passed through unchanged, because presigned storage URLs
// are signed for the public host. Local development only.
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func main() {
	api := proxy(env("API_URL", "http://api:8080"))
	web := proxy(env("WEB_URL", "http://web:3000"))
	storage := proxy(env("STORAGE_URL", "http://minio:9000"))
	bucket := "/" + strings.Trim(env("STORAGE_BUCKET", "gamics-local"), "/") + "/"
	admin := spa(env("ADMIN_DIR", "/admin"))

	mux := http.NewServeMux()
	mux.Handle("/v1/", api)
	mux.Handle("/healthz", api)
	mux.Handle("/readyz", api)
	mux.Handle(bucket, storage)
	mux.Handle("/admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))
	mux.Handle("/admin/", http.StripPrefix("/admin", admin))
	mux.Handle("/", web)

	address := ":" + env("PORT", "8080")
	log.Printf("tunnel router on %s (storage bucket %s)", address, bucket)
	log.Fatal(http.ListenAndServe(address, mux))
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// proxy forwards to target and keeps the incoming Host header.
func proxy(target string) http.Handler {
	parsed, err := url.Parse(target)
	if err != nil {
		log.Fatalf("bad upstream %q: %v", target, err)
	}
	forward := httputil.NewSingleHostReverseProxy(parsed)
	forward.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream %s for %s: %v", parsed.Host, r.URL.Path, err)
		http.Error(w, "The local service is not running.", http.StatusBadGateway)
	}
	return forward
}

// spa serves a built single-page app: real files as they are, any other path
// as index.html so the app's router can handle it.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
		if info, err := os.Stat(name); err != nil || info.IsDir() {
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
