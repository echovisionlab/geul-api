package proxy

import (
	"net/http"
	"time"
)

const upstreamRequestTimeout = 30 * time.Second

func newUpstreamHTTPClient() *http.Client {
	return &http.Client{Timeout: upstreamRequestTimeout}
}
