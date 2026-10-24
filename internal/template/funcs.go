package template

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

// argKind is what a function accepts as its single argument.
type argKind int

const (
	argNone    argKind = iota
	argLiteral         // quoted string, number, true / false / null
	argLayout          // one of timeLayouts
	argSecret          // secret.NAME
)

const (
	fnDefault = "default"

	layoutRFC3339 = "rfc3339"
	layoutUnix    = "unix"
	layoutUnixMS  = "unix_ms"
)

var timeLayouts = []string{layoutRFC3339, layoutUnix, layoutUnixMS}

type function struct {
	arg   argKind
	apply func(in, arg any, en *env) (any, error)
}

// functions is the fixed set of 12 built-ins (§7.1).
var functions = map[string]function{
	"lower":              stringFn(strings.ToLower),
	"upper":              stringFn(strings.ToUpper),
	"trim":               stringFn(strings.TrimSpace),
	"sha256":             stringFn(sha256Hex),
	"base64":             stringFn(func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }),
	"urlencode":          stringFn(url.QueryEscape),
	fnDefault:            {arg: argLiteral, apply: applyDefault},
	"format_time":        {arg: argLayout, apply: applyFormatTime},
	"to_string":          {arg: argNone, apply: func(in, _ any, _ *env) (any, error) { return toText(in) }},
	"json":               {arg: argNone, apply: applyJSON},
	"hmac_sha256_hex":    hmacFn(hex.EncodeToString),
	"hmac_sha256_base64": hmacFn(base64.StdEncoding.EncodeToString),
}

// stringFn wraps a string → string function; other inputs are stringified
// first, as in embedded text.
func stringFn(f func(string) string) function {
	return function{arg: argNone, apply: func(in, _ any, _ *env) (any, error) {
		s, err := toText(in)
		if err != nil {
			return nil, err
		}
		return f(s), nil
	}}
}

func hmacFn(encode func([]byte) string) function {
	return function{arg: argSecret, apply: func(in, arg any, en *env) (any, error) {
		name := arg.(string)
		key, ok := en.vars.Secrets[name]
		if !ok {
			return nil, badArgumentf("secret %s is not declared", name)
		}
		s, err := toText(in)
		if err != nil {
			return nil, err
		}
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(s))
		return encode(mac.Sum(nil)), nil
	}}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func applyDefault(in, arg any, _ *env) (any, error) {
	if _, isMissing := in.(missing); isMissing || in == nil {
		return arg, nil
	}
	return in, nil
}

func applyFormatTime(in, arg any, _ *env) (any, error) {
	t, ok := asTime(in)
	if !ok {
		return nil, badArgumentf("format_time needs a time or an RFC 3339 string")
	}
	switch arg.(string) {
	case layoutUnix:
		return t.Unix(), nil
	case layoutUnixMS:
		return t.UnixMilli(), nil
	default:
		return formatRFC3339(t), nil
	}
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}

func applyJSON(in, _ any, _ *env) (any, error) {
	b, err := MarshalJSON(toJSONValue(in))
	if err != nil {
		return nil, err
	}
	return string(b), nil
}
