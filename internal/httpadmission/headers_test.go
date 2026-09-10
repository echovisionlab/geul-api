package httpadmission

import (
	"net/http"
	"testing"
)

func TestCopy(t *testing.T) {
	src := http.Header{}
	for _, name := range []string{Challenge, RetryAfter, RateLimit, RateLimitPolicy} {
		src.Add(name, " value ")
		src.Add(name, " ")
	}
	src.Set("X-Secret", "private")
	dst := http.Header{}
	Copy(dst, src)
	if len(dst) != 4 {
		t.Fatal(dst)
	}
	for _, name := range []string{Challenge, RetryAfter, RateLimit, RateLimitPolicy} {
		if dst.Get(name) != "value" {
			t.Fatal(dst)
		}
	}
	Copy(http.Header{}, http.Header{})
}
