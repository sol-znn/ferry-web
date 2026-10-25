//go:build !(js && wasm)

package httpx

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoAcceptsCompleteBodiesAtTheLimit(t *testing.T) {
	for _, size := range []int{0, 1, MaxBody} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			want := bytes.Repeat([]byte{'x'}, size)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Length", fmt.Sprint(size))
				_, _ = w.Write(want)
			}))
			defer srv.Close()
			got, err := Do(context.Background(), Request{Method: "GET", URL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != http.StatusOK || !bytes.Equal(got.Body, want) {
				t.Fatalf("response was changed: status=%d bytes=%d", got.Status, len(got.Body))
			}
		})
	}
}

func TestDoRejectsOversizedBodiesInsteadOfReturningATruncatedSuccess(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprintf("Content-Length=%v", declared), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if declared {
					w.Header().Set("Content-Length", fmt.Sprint(MaxBody+1))
				} else {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush() // enforce chunked/unknown length
				}
				chunk := bytes.Repeat([]byte{'x'}, 64<<10)
				for remaining := MaxBody + 1; remaining > 0; {
					n := min(remaining, len(chunk))
					if _, err := w.Write(chunk[:n]); err != nil {
						return
					}
					remaining -= n
				}
			}))
			defer srv.Close()
			got, err := Do(context.Background(), Request{Method: "GET", URL: srv.URL})
			if err == nil || !strings.Contains(err.Error(), "byte limit") {
				t.Fatalf("oversized response did not report its bound: %v", err)
			}
			if got != nil {
				t.Fatal("an oversized body was returned as a partial successful response")
			}
		})
	}
}

func TestDoDoesNotTrustAnIncompleteDeclaredBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	got, err := Do(context.Background(), Request{Method: "GET", URL: srv.URL})
	if err == nil || got != nil {
		t.Fatal("an incomplete response was returned as a successful body")
	}
}
