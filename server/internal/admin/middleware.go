package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// AdminAuth returns middleware that validates Authorization: Bearer <secret>
// using constant-time comparison. Returns 401 on failure.
func AdminAuth(secret string) func(http.Handler) http.Handler {
	// Compare fixed-size SHA-256 digests so both operands are always 32 bytes:
	// a raw ConstantTimeCompare short-circuits on length mismatch and leaks the
	// secret's length via timing. Digest is precomputed once here.
	secretDigest := sha256.Sum256([]byte(secret))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := []byte(strings.TrimPrefix(auth, "Bearer "))
			tokenDigest := sha256.Sum256(token)
			if subtle.ConstantTimeCompare(tokenDigest[:], secretDigest[:]) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

