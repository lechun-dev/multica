package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// MissionExchangeAuth requires shared replay protection; an unavailable Redis
// fails closed, including in multi-replica deployments. Never log signed bodies.
func MissionExchangeAuth(secret string, rdb redis.UniversalClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(secret) < 32 || rdb == nil {
				writeError(w, http.StatusServiceUnavailable, "mission exchange unavailable")
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid exchange body")
				return
			}
			rawTime, nonce := r.Header.Get("X-Mission-Timestamp"), r.Header.Get("X-Mission-Nonce")
			if !validMissionSignature(secret, r, body, time.Now()) {
				writeError(w, http.StatusUnauthorized, "invalid service signature")
				return
			}
			ok, err := rdb.SetNX(r.Context(), "mission:exchange:nonce:"+nonce, rawTime, 2*time.Minute).Result()
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "mission exchange unavailable")
				return
			}
			if !ok {
				writeError(w, http.StatusUnauthorized, "exchange replay rejected")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

func validMissionSignature(secret string, r *http.Request, body []byte, now time.Time) bool {
	rawTime, nonce := r.Header.Get("X-Mission-Timestamp"), r.Header.Get("X-Mission-Nonce")
	timestamp, err := strconv.ParseInt(rawTime, 10, 64)
	if err != nil || timestamp < now.Unix()-60 || timestamp > now.Unix()+60 {
		return false
	}
	nonceBytes, err := hex.DecodeString(nonce)
	if err != nil || len(nonceBytes) != 16 {
		return false
	}
	signature, err := hex.DecodeString(r.Header.Get("X-Mission-Signature"))
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(r.Method + "\n" + r.URL.RequestURI() + "\n" + rawTime + "\n" + nonce + "\n"))
	mac.Write(body)
	return hmac.Equal(signature, mac.Sum(nil))
}
