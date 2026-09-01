package middleware

import "net/http"

// Apply composes multiple HTTP middlewares in order.
func Apply(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		middleware := middlewares[i]
		handler = middleware(handler)
	}
	return handler
}
