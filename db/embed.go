// Package db holds the PostgreSQL schema migrations, embedded for goose.
package db

import "embed"

// MigrationsDir is the directory inside Migrations that holds goose SQL files.
const MigrationsDir = "migrations"

// Migrations contains every goose SQL migration, for use with goose.SetBaseFS.
//
//go:embed migrations/*.sql
var Migrations embed.FS
