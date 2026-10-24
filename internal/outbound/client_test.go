package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

func TestSendReturnsResponse(t *testing.T) {
	// Arrange
	var gotMethod, gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotHeader, gotBody = r.Method, r.Header.Get("X-A"), string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(NewTransport())

	// Act
	resp, err := c.Send(context.Background(), Request{
		Method: http.MethodPut, URL: srv.URL, Headers: map[string]string{"X-A": "1"}, Body: []byte("hello"),
	}, testTimeout)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotHeader != "1" || gotBody != "hello" {
		t.Errorf("server saw %s %q %q", gotMethod, gotHeader, gotBody)
	}
	want := Response{StatusCode: 202, ContentType: "application/json", RetryAfter: "7", Body: []byte(`{"ok":true}`), BodyComplete: true}
	if resp.StatusCode != want.StatusCode || resp.ContentType != want.ContentType ||
		resp.RetryAfter != want.RetryAfter || string(resp.Body) != string(want.Body) || !resp.BodyComplete {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	// Act
	resp, err := NewClient(NewTransport()).Send(context.Background(), Request{Method: http.MethodPost, URL: srv.URL}, testTimeout)

	// Assert
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("resp = %+v, err = %v, want 302", resp, err)
	}
}

func TestSendLimitsResponseRead(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseReadBytes+10)))
	}))
	defer srv.Close()

	// Act
	resp, err := NewClient(NewTransport()).Send(context.Background(), Request{Method: http.MethodPost, URL: srv.URL}, testTimeout)

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if resp.BodyComplete || len(resp.Body) != maxResponseReadBytes {
		t.Fatalf("complete = %v, len = %d", resp.BodyComplete, len(resp.Body))
	}
}

func TestSendClassifiesFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(testTimeout):
		}
	}))
	defer slow.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name      string
		transport http.RoundTripper
		url       string
		timeout   time.Duration
		want      error
	}{
		{"timeout", NewTransport(), slow.URL, 50 * time.Millisecond, ErrNetwork},
		{"connection refused", NewTransport(), closedURL, testTimeout, ErrNetwork},
		{"egress proxy down", proxyTransport(t, closedURL), "http://vendor.example.com/", testTimeout, ErrInfra},
		{"unsupported scheme", NewTransport(), "ftp://x.example.com/", testTimeout, ErrInvalidRequest},
		{"unparsable url", NewTransport(), "http://[::1", testTimeout, ErrInvalidRequest},
		{"bad method", NewTransport(), "http://x.example.com/", testTimeout, ErrInvalidRequest},
		{"control character in header", NewTransport(), "http://x.example.com/", testTimeout, ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			method := http.MethodPost
			if tt.name == "bad method" {
				method = "BAD METHOD"
			}
			var headers map[string]string
			if tt.name == "control character in header" {
				headers = map[string]string{"X-A": "a\r\nInjected: 1"}
			}

			// Act
			_, err := NewClient(tt.transport).Send(context.Background(), Request{Method: method, URL: tt.url, Headers: headers}, tt.timeout)

			// Assert
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSendErrorsDoNotQuoteTheURL(t *testing.T) {
	// Arrange
	closed := httptest.NewServer(http.NotFoundHandler())
	target := closed.URL + "/hook?api_key=" + fakeSecret
	closed.Close()

	// Act
	_, err := NewClient(NewTransport()).Send(context.Background(), Request{Method: http.MethodPost, URL: target}, testTimeout)

	// Assert
	if err == nil || strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("err = %v, want an error without the query", err)
	}
}

func TestReadLimitedReportsReadErrors(t *testing.T) {
	// Act
	body, complete := readLimited(io.MultiReader(strings.NewReader("ab"), errReader{}))

	// Assert
	if complete || string(body) != "ab" {
		t.Fatalf("body = %q, complete = %v", body, complete)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Err: errors.New("reset")}
}

func proxyTransport(t *testing.T, proxy string) *http.Transport {
	t.Helper()
	u, err := url.Parse(proxy)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTransport()
	tr.Proxy = http.ProxyURL(u)
	return tr
}
