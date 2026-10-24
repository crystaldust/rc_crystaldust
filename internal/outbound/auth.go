package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"messagehub/internal/config"
	"messagehub/internal/secrets"
)

// tokenRequestTimeout bounds one call to an OAuth2 token endpoint.
const tokenRequestTimeout = 10 * time.Second

// AuthorizationHeader is the header OAuth2 access tokens are sent in.
const AuthorizationHeader = "Authorization"

type tokenKey struct {
	destinationID string
	configHash    string
}

// TokenProvider caches one OAuth2 token source per (destination, config
// hash). A source reuses its token until shortly before it expires. When
// the config changes, the next call for a destination replaces its source.
// Token requests and responses are never recorded (ADR-0008 D2).
type TokenProvider struct {
	secrets secrets.Resolver
	client  *http.Client

	mu      sync.Mutex
	sources map[tokenKey]oauth2.TokenSource
}

// NewTokenProvider returns a provider resolving client credentials through r
// and calling token endpoints over transport.
func NewTokenProvider(r secrets.Resolver, transport http.RoundTripper) *TokenProvider {
	return &TokenProvider{
		secrets: r,
		client:  &http.Client{Transport: transport, Timeout: tokenRequestTimeout},
		sources: make(map[tokenKey]oauth2.TokenSource),
	}
}

// Authorization returns the Authorization header value for a destination,
// or "" when auth.type is none. Failures wrap ErrInfra: the vendor was not
// called, so the attempt consumes no budget (§5.3).
func (p *TokenProvider) Authorization(destinationID, configHash string, auth config.Auth) (string, error) {
	if auth.Type != config.AuthOAuth2ClientCredentials {
		return "", nil
	}
	src, err := p.source(tokenKey{destinationID: destinationID, configHash: configHash}, auth)
	if err != nil {
		return "", err
	}
	tok, err := src.Token()
	if err != nil {
		return "", fmt.Errorf("%w: oauth2 token for %s: %s", ErrInfra, destinationID, tokenErrorText(err))
	}
	return tok.Type() + " " + tok.AccessToken, nil
}

// tokenErrorText describes a token failure without the token endpoint's
// response body, which is never recorded (ADR-0008 D2).
func tokenErrorText(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		if re.ErrorCode != "" {
			return fmt.Sprintf("token endpoint returned %d (%s)", status, re.ErrorCode)
		}
		return fmt.Sprintf("token endpoint returned %d", status)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "token endpoint unreachable: " + urlErr.Err.Error()
	}
	return err.Error()
}

func (p *TokenProvider) source(key tokenKey, auth config.Auth) (oauth2.TokenSource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if src, ok := p.sources[key]; ok {
		return src, nil
	}
	clientID, err := p.secrets.Resolve(auth.ClientIDRef)
	if err != nil {
		return nil, fmt.Errorf("%w: oauth2 client id for %s: %w", ErrInfra, key.destinationID, err)
	}
	clientSecret, err := p.secrets.Resolve(auth.ClientSecretRef)
	if err != nil {
		return nil, fmt.Errorf("%w: oauth2 client secret for %s: %w", ErrInfra, key.destinationID, err)
	}
	cc := clientcredentials.Config{
		ClientID: clientID, ClientSecret: clientSecret,
		TokenURL: auth.TokenURL, Scopes: auth.Scopes,
	}
	// The source keeps this context for every refresh, so it must not be a
	// per-delivery context; tokenRequestTimeout bounds each call instead.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, p.client)
	src := cc.TokenSource(ctx)
	for k := range p.sources {
		if k.destinationID == key.destinationID {
			delete(p.sources, k)
		}
	}
	p.sources[key] = src
	return src, nil
}
