package template

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type tokenKind int

const (
	tokIdent  tokenKind = iota // a single word: now, rfc3339, true, lower
	tokPath                    // dotted words: event.payload.items.0.sku
	tokNumber                  // 0, -1.5
	tokString                  // "quoted"
	tokPipe                    // |
)

// token is one lexical element of an expression. value holds the decoded
// literal for tokNumber (json.Number) and tokString (string).
type token struct {
	kind  tokenKind
	text  string
	value any
}

const (
	openDelim  = "{{"
	closeDelim = "}}"
)

// findClose returns the index in s of the "}}" that ends an expression whose
// body starts at s[0], skipping over quoted strings; -1 when unterminated.
func findClose(s string) int {
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && strings.HasPrefix(s[i:], closeDelim):
			return i
		}
	}
	return -1
}

// lexExpr splits the body of one {{ … }} expression into tokens.
func lexExpr(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '|':
			out = append(out, token{kind: tokPipe, text: "|"})
			i++
		case c == '"':
			tok, n, err := lexString(s[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, tok)
			i += n
		case c == '-' || isDigit(c):
			tok, n, err := lexNumber(s[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, tok)
			i += n
		case isWordStart(c):
			n := wordLen(s[i:])
			out = append(out, wordToken(s[i:i+n]))
			i += n
		default:
			return nil, syntaxErrorf("unexpected character %q", c)
		}
	}
	return out, nil
}

func lexString(s string) (token, int, error) {
	end := findStringEnd(s)
	if end < 0 {
		return token{}, 0, syntaxErrorf("unterminated string literal")
	}
	text := s[:end+1]
	v, err := strconv.Unquote(text)
	if err != nil {
		return token{}, 0, syntaxErrorf("invalid string literal %s", text)
	}
	return token{kind: tokString, text: text, value: v}, end + 1, nil
}

// findStringEnd returns the index of the closing quote of the string that
// starts at s[0], or -1.
func findStringEnd(s string) int {
	escaped := false
	for i := 1; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			return i
		}
	}
	return -1
}

func lexNumber(s string) (token, int, error) {
	n := 1
	for n < len(s) && isNumberChar(s[n]) {
		n++
	}
	text := s[:n]
	var num json.Number
	if err := json.Unmarshal([]byte(text), &num); err != nil {
		return token{}, 0, syntaxErrorf("invalid number literal %q", text)
	}
	return token{kind: tokNumber, text: text, value: num}, n, nil
}

func wordToken(w string) token {
	if strings.Contains(w, ".") {
		return token{kind: tokPath, text: w}
	}
	return token{kind: tokIdent, text: w}
}

func wordLen(s string) int {
	n := 0
	for n < len(s) && isWordChar(s[n]) {
		n++
	}
	return n
}

func isDigit(c byte) bool     { return c >= '0' && c <= '9' }
func isLetter(c byte) bool    { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isWordStart(c byte) bool { return isLetter(c) || c == '_' }

// isWordChar admits path segments such as payload keys with '-' and array
// indexes.
func isWordChar(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '-' || c == '.'
}

func isNumberChar(c byte) bool {
	return isDigit(c) || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'
}

func syntaxErrorf(format string, args ...any) *Error {
	return &Error{Class: ErrorSyntax, Detail: fmt.Sprintf(format, args...)}
}
