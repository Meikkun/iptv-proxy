package server

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// Recognize Xtream account credentials, not a hostname-wide viewer pool.
// Unknown URL layouts remain outside this policy.
func relayAccountKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	var credentials []string
	switch {
	case len(parts) == 4 && (parts[0] == "live" || parts[0] == "movie" || parts[0] == "series"):
		credentials = parts[1:3]
	case len(parts) == 6 && parts[0] == "timeshift":
		credentials = parts[1:3]
	case len(parts) == 3 && parts[0] != "play" && parts[0] != "hls" && parts[0] != "hlsr":
		credentials = parts[:2]
	default:
		return ""
	}
	user, userErr := url.PathUnescape(credentials[0])
	password, passwordErr := url.PathUnescape(credentials[1])
	if userErr != nil || passwordErr != nil || user == "" || password == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	identity := fmt.Sprintf("%q", []string{u.Scheme, strings.ToLower(u.Hostname()), port, user, password})
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func relayRequestIdentity(rawURL string, headers http.Header) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return relayKey(rawURL, headers)
	}
	return relayKey(u.User.String(), headers)
}

// A direct stream cannot participate in channel substitution. Refuse recognized
// account streams in protective mode rather than silently bypassing its limit.
func (c *Config) rejectUnsharedAccountStream(ctx *gin.Context, upstream *url.URL) bool {
	if c.relay == nil || !c.relay.config.ExistingChannelWins || relayAccountKey(upstream.String()) == "" {
		return false
	}
	ctx.AbortWithStatusJSON(http.StatusConflict, gin.H{
		"error": "account protection requires a shared live TS stream without seeking",
	})
	return true
}
