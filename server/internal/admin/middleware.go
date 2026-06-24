package admin

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// AdminAuth returns middleware that validates Authorization: Bearer <secret>
// using constant-time comparison. Returns 401 on failure.
func AdminAuth(secret string) func(http.Handler) http.Handler {
	secretBytes := []byte(secret)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := []byte(strings.TrimPrefix(auth, "Bearer "))
			if subtle.ConstantTimeCompare(token, secretBytes) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

