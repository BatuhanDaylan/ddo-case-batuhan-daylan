package auth

import (
	"net"
	"net/http"
	"strconv"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
