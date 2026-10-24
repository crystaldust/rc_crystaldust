// Package outbound performs the HTTP side of a delivery: the shared vendor
// client with timeouts and read limits (§7.3 error classes), OAuth2 token
// caching (§11) and the masked, truncated attempt records (§10, ADR-0008).
// It depends on config, secrets, template and domain.
package outbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Transport limits shared by every vendor call in the process.
const (
	maxIdleConns          = 512
	maxIdleConnsPerHost   = 64
	idleConnTimeout       = 90 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = time.Second
	// maxResponseReadBytes bounds how much of a response is read; the rest
	// is discarded and the body counts as incomplete.
	maxResponseReadBytes = 1 << 20
)

var (
	// ErrInfra marks a failure on the Hub's side before the request reached
	// the vendor (egress proxy, token endpoint, secret lookup); it does not
	// consume retry budget (§5.3).
	ErrInfra = errors.New("infrastructure failure")
	// ErrNetwork marks a connection, DNS, TLS or timeout failure (§7.3).
	ErrNetwork = errors.New("network failure")
	// ErrInvalidRequest marks a rendered request that cannot be sent, such as
	// a URL that does not parse or is not http / https.
	ErrInvalidRequest = errors.New("invalid request")
)

// Request is one rendered vendor request.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Response is what the vendor returned. Body holds at most
// maxResponseReadBytes; BodyComplete is false when more was available or
// reading failed part way.
type Response struct {
	StatusCode   int
	ContentType  string
	RetryAfter   string
	Body         []byte
	BodyComplete bool
}

// Client sends vendor requests over one shared transport. Redirects are
// never followed: a 3xx is classified like any other status.
type Client struct {
	http *http.Client
}

// NewTransport returns the process-wide transport for vendor calls.
func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = maxIdleConns
	t.MaxIdleConnsPerHost = maxIdleConnsPerHost
	t.IdleConnTimeout = idleConnTimeout
	t.TLSHandshakeTimeout = tlsHandshakeTimeout
	t.ExpectContinueTimeout = expectContinueTimeout
	return t
}

// NewClient returns a client over transport.
func NewClient(transport http.RoundTripper) *Client {
	return &Client{http: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Send performs one request bounded by timeout. Errors wrap ErrInvalidRequest,
// ErrInfra or ErrNetwork.
func (c *Client) Send(ctx context.Context, req Request, timeout time.Duration) (Response, error) {
	httpReq, err := buildRequest(req)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.http.Do(httpReq.WithContext(ctx))
	if err != nil {
		return Response{}, classifyTransportError(err)
	}
	defer resp.Body.Close()
	body, complete := readLimited(resp.Body)
	return Response{
		StatusCode:   resp.StatusCode,
		ContentType:  resp.Header.Get("Content-Type"),
		RetryAfter:   resp.Header.Get("Retry-After"),
		Body:         body,
		BodyComplete: complete,
	}, nil
}

func buildRequest(req Request) (*http.Request, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, fmt.Errorf("%w: url does not parse", ErrInvalidRequest)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: url scheme must be http or https", ErrInvalidRequest)
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequest(req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	for name, v := range req.Headers {
		if !validHeaderValue(v) {
			return nil, fmt.Errorf("%w: header %s has a control character in its value", ErrInvalidRequest, name)
		}
		httpReq.Header.Set(name, v)
	}
	return httpReq, nil
}

// validHeaderValue rejects CR, LF and other control characters (RFC 9110
// §5.5), which a value rendered from event data may contain; net/http
// would otherwise fail the request like a network error.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < ' ' && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// classifyTransportError separates egress proxy failures (the request never
// left the Hub) from network failures. The *url.Error wrapper is dropped
// because it quotes the full URL, whose query may carry a secret; what
// remains names at most the host.
func classifyTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "proxyconnect" {
		return fmt.Errorf("%w: egress proxy: %w", ErrInfra, err)
	}
	return fmt.Errorf("%w: %w", ErrNetwork, err)
}

func readLimited(r io.Reader) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseReadBytes+1))
	if err != nil {
		return body, false
	}
	if len(body) > maxResponseReadBytes {
		return body[:maxResponseReadBytes], false
	}
	return body, true
}
