package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"messagehub/internal/domain"
)

// Limits on JSON request bodies beyond what encoding/json enforces
// (domain-model.md §13.1). PostgreSQL text and jsonb reject U+0000 and
// invalid UTF-8, and jsonb numeric overflows on huge numbers, so these are
// rejected here as 400 instead of failing in the database as 500.
const (
	maxJSONDepth          = 1000
	maxJSONNumberLen      = 100
	maxJSONNumberExponent = 1000
)

// errStopWalk ends the walk on malformed JSON; domain.ParseEnvelope reports
// it with a proper message.
var errStopWalk = errors.New("stop walk")

// CheckStrictJSON rejects a body that encoding/json would accept but the
// Hub must not: invalid UTF-8, a repeated key in any object (encoding/json
// keeps the last value silently), U+0000 in any key or string, a number
// outside maxJSONNumberLen / maxJSONNumberExponent, or nesting deeper than
// maxJSONDepth. Problems name the JSON path. Malformed JSON is left to the
// envelope parser.
func CheckStrictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return problem("", "body must be valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err := walkJSON(dec, "", 0)
	var verr *domain.ValidationError
	if errors.As(err, &verr) {
		return verr
	}
	return nil
}

func problem(field, reason string) *domain.ValidationError {
	return &domain.ValidationError{Problems: []domain.FieldError{{Field: field, Reason: reason}}}
}

func walkJSON(dec *json.Decoder, path string, depth int) error {
	if depth > maxJSONDepth {
		return problem(path, fmt.Sprintf("nesting is deeper than %d levels", maxJSONDepth))
	}
	tok, err := dec.Token()
	if err != nil {
		return errStopWalk
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '{' {
			return walkObject(dec, path, depth)
		}
		return walkArray(dec, path, depth)
	case string:
		return checkString(path, v)
	case json.Number:
		return checkNumber(path, v)
	default:
		return nil
	}
}

func walkObject(dec *json.Decoder, path string, depth int) error {
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errStopWalk
		}
		key, _ := tok.(string)
		field := joinPath(path, key)
		if err := checkString(field, key); err != nil {
			return err
		}
		if seen[key] {
			return problem(field, "duplicate key")
		}
		seen[key] = true
		if err := walkJSON(dec, field, depth+1); err != nil {
			return err
		}
	}
	return closeToken(dec)
}

func walkArray(dec *json.Decoder, path string, depth int) error {
	for i := 0; dec.More(); i++ {
		if err := walkJSON(dec, path+"["+strconv.Itoa(i)+"]", depth+1); err != nil {
			return err
		}
	}
	return closeToken(dec)
}

func closeToken(dec *json.Decoder) error {
	if _, err := dec.Token(); err != nil {
		return errStopWalk
	}
	return nil
}

func checkString(path, s string) error {
	if strings.ContainsRune(s, 0) {
		return problem(path, "must not contain the NUL character (U+0000)")
	}
	return nil
}

// checkNumber bounds a JSON number literal so jsonb numeric can store it.
func checkNumber(path string, n json.Number) error {
	s := n.String()
	if len(s) > maxJSONNumberLen {
		return problem(path, fmt.Sprintf("number literal is longer than %d characters", maxJSONNumberLen))
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp > maxJSONNumberExponent || exp < -maxJSONNumberExponent {
			return problem(path, fmt.Sprintf("number exponent is outside ±%d", maxJSONNumberExponent))
		}
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
