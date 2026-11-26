//go:build diagtest

package client

import (
	"net/http"
	"time"

	"sharetoken/internal/diag"
)

// Builds with -tags diagtest crash on request, for the end-to-end tests of
// crash reports on real systems: POST /api/v1/diagnostics/crash?kind=…
//
//	goroutine  an unrecovered panic in a plain goroutine: the process dies
//	wrapped    the same in a goroutine started with diag.Go
//	fatal      concurrent map writes: a fatal runtime error
//	handler    a panic in this handler: net/http recovers it, the process lives
func init() {
	testCrashRoutes = func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/diagnostics/crash", func(w http.ResponseWriter, r *http.Request) {
			kind := r.URL.Query().Get("kind")
			if kind == "handler" {
				crashInHandler(7)
			}
			writeJSON(w, http.StatusAccepted, map[string]string{"kind": kind})
			go func() {
				time.Sleep(300 * time.Millisecond)
				switch kind {
				case "goroutine":
					go crashInGoroutine(5)
				case "wrapped":
					diag.Go("testcrash", func() { crashInWrapped(6) })
				case "fatal":
					m := map[int]int{}
					for i := range 8 {
						go func() {
							for j := 0; ; j++ {
								m[j%64+i] = j
							}
						}()
					}
				}
			}()
		})
	}
}

// Three functions, so that each kind is a problem of its own (fingerprint).

//go:noinline
func crashInHandler(n int) int {
	var s []int
	return s[n]
}

//go:noinline
func crashInGoroutine(n int) int {
	var s []int
	return s[n]
}

//go:noinline
func crashInWrapped(n int) int {
	var s []int
	return s[n]
}
