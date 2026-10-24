package config

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"250ms", 250 * time.Millisecond, false},
		{"5s", 5 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"1h", time.Hour, false},
		{"0s", 0, false},
		{"5", 0, true},
		{"1.5s", 0, true},
		{"-1s", 0, true},
		{"5 s", 0, true},
		{"1d", 0, true},
		{"99999999999h", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			// Act
			got, err := ParseDuration(tt.in)

			// Assert
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && (got.Std() != tt.want || got.String() != tt.in) {
				t.Fatalf("got %v (%s), want %v (%s)", got.Std(), got, tt.want, tt.in)
			}
		})
	}
}

func TestDurationOverflowIsRangeError(t *testing.T) {
	_, err := ParseDuration("99999999999h")
	if !errors.Is(err, errDurationRange) {
		t.Fatalf("err = %v, want errDurationRange", err)
	}
}

func TestDurationJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", `"5s"`, false},
		{"number", `5`, true},
		{"bad format", `"5x"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := json.Unmarshal([]byte(tt.in), &d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			out, _ := json.Marshal(d)
			if string(out) != tt.in {
				t.Fatalf("round trip = %s, want %s", out, tt.in)
			}
		})
	}
}

func TestDurationYAML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"valid", "d: 30s", 30 * time.Second, false},
		{"bad format", "d: 30 seconds", 0, true},
		{"not scalar", "d: [1, 2]", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v struct {
				D Duration `yaml:"d"`
			}
			err := yaml.Unmarshal([]byte(tt.in), &v)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if v.D.Std() != tt.want {
				t.Fatalf("got %v, want %v", v.D.Std(), tt.want)
			}
		})
	}
}

func TestStatusMatcherString(t *testing.T) {
	tests := []struct {
		matcher StatusMatcher
		want    string
	}{
		{StatusMatcher{Code: 200}, "200"},
		{StatusMatcher{Code: 429}, "429"},
		{StatusMatcher{Class: 2}, "2xx"},
		{StatusMatcher{Class: 5}, "5xx"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.matcher.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusMatcher(t *testing.T) {
	tests := []struct {
		json    string
		code    int
		matches bool
		wantErr bool
	}{
		{`200`, 200, true, false},
		{`200`, 201, false, false},
		{`"2xx"`, 204, true, false},
		{`"2xx"`, 302, false, false},
		{`"5xx"`, 503, true, false},
		{`"xx"`, 0, false, true},
		{`"22xx"`, 0, false, true},
		{`"2yy"`, 0, false, true},
		{`true`, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.json, func(t *testing.T) {
			// Act
			var m StatusMatcher
			err := json.Unmarshal([]byte(tt.json), &m)

			// Assert
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got := m.Matches(tt.code); got != tt.matches {
				t.Fatalf("Matches(%d) = %v, want %v", tt.code, got, tt.matches)
			}
			out, _ := json.Marshal(m)
			if string(out) != tt.json {
				t.Fatalf("marshal = %s, want %s", out, tt.json)
			}
		})
	}
}
