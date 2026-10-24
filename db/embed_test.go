package db

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

func TestMigrationsEmbedInitWithGooseMarkers(t *testing.T) {
	// Arrange
	name := path.Join(MigrationsDir, "00001_init.sql")

	// Act
	content, err := fs.ReadFile(Migrations, name)

	// Assert
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	for _, marker := range []string{"-- +goose Up", "-- +goose Down"} {
		if !strings.Contains(string(content), marker) {
			t.Errorf("%s is missing marker %q", name, marker)
		}
	}
	up := strings.Index(string(content), "-- +goose Up")
	down := strings.Index(string(content), "-- +goose Down")
	if up > down {
		t.Errorf("%s: Up marker must precede Down marker", name)
	}
}
