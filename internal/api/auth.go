package api

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	"messagehub/internal/apikey"
	"messagehub/internal/config"
	"messagehub/internal/secrets"
)

const bearerScheme = "bearer"

// principalKeys is one principal with the 1–2 key hashes it accepts.
type principalKeys struct {
	principal config.Principal
	hashes    [][32]byte
}

// keyIndex is built once per ConfigHash and replaced atomically, so a
// config reload takes effect on the next request (plan Task 6).
type keyIndex struct {
	configHash string
	keys       []principalKeys
}

type authenticator struct {
	config   func() *config.Config
	resolver secrets.Resolver
	log      *slog.Logger
	cache    atomic.Pointer[keyIndex]
}

func newAuthenticator(cfg func() *config.Config, r secrets.Resolver, log *slog.Logger) *authenticator {
	return &authenticator{config: cfg, resolver: r, log: log}
}

func (a *authenticator) index() *keyIndex {
	cfg := a.config()
	if cur := a.cache.Load(); cur != nil && cur.configHash == cfg.Hash() {
		return cur
	}
	built := a.build(cfg)
	a.cache.Store(built)
	return built
}

// build resolves every principal's hashes. A principal whose variable is
// missing or malformed cannot authenticate; the others keep working. A hash
// shared by two principals would make the key ambiguous, so every principal
// holding it is disabled.
func (a *authenticator) build(cfg *config.Config) *keyIndex {
	idx := &keyIndex{configHash: cfg.Hash()}
	owners := map[[32]byte]int{}
	var candidates []principalKeys
	for _, p := range cfg.Principals() {
		value, err := a.resolver.Resolve(p.APIKeySHA256Ref)
		var hashes [][32]byte
		if err == nil {
			hashes, err = apikey.ParseHashes(value)
		}
		if err != nil {
			a.unavailable(p.ID, idx.configHash, err.Error())
			continue
		}
		for _, h := range hashes {
			owners[h]++
		}
		candidates = append(candidates, principalKeys{principal: p, hashes: hashes})
	}
	for _, pk := range candidates {
		if slices.ContainsFunc(pk.hashes, func(h [32]byte) bool { return owners[h] > 1 }) {
			a.unavailable(pk.principal.ID, idx.configHash, "key hash is shared with another principal")
			continue
		}
		idx.keys = append(idx.keys, pk)
	}
	return idx
}

func (a *authenticator) unavailable(principalID, configHash, reason string) {
	a.log.Error("auth.principal_unavailable", slog.String("principal_id", principalID),
		slog.String("config_hash", configHash), slog.String("error", reason))
}

// authenticate maps "Authorization: Bearer <key>" to a principal. Every
// principal is compared in constant time, without stopping at a match, so
// timing does not reveal which principal or position matched.
func (a *authenticator) authenticate(r *http.Request) (config.Principal, bool) {
	scheme, key, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	key = strings.TrimSpace(key)
	if !ok || !strings.EqualFold(scheme, bearerScheme) || key == "" {
		return config.Principal{}, false
	}
	var found config.Principal
	matched := false
	for _, pk := range a.index().keys {
		if apikey.Matches(key, pk.hashes) && !matched {
			found, matched = pk.principal, true
		}
	}
	return found, matched
}

// rejected logs a refused request as a security event, without the key.
func (a *authenticator) rejected(r *http.Request, reason, principalID string) {
	a.log.Warn("auth.rejected",
		slog.String("request_id", requestID(r.Context())),
		slog.String("reason", reason),
		slog.String("route", r.Pattern),
		slog.String("remote_addr", r.RemoteAddr),
		slog.String("principal_id", principalID))
}

// principalHandler handles a request from an authenticated principal.
type principalHandler func(w http.ResponseWriter, r *http.Request, p config.Principal)

// require authenticates the request (401) and checks the role (403).
func (a *authenticator) require(roles []config.Role, h principalHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.authenticate(r)
		if !ok {
			a.rejected(r, "unauthenticated", "")
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "missing or invalid API key", nil)
			return
		}
		infoFrom(r.Context()).principalID = p.ID
		if !slices.Contains(roles, p.Role) {
			a.rejected(r, "forbidden", p.ID)
			writeError(w, http.StatusForbidden, codeForbidden, "role "+string(p.Role)+" may not perform this operation", nil)
			return
		}
		h(w, r, p)
	}
}
