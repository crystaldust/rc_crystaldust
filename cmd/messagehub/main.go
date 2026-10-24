// Command messagehub is the single Message Hub executable: configuration
// checks, migrations, API key generation and the api / worker roles.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"messagehub/internal/apikey"
	"messagehub/internal/app"
	"messagehub/internal/config"
	"messagehub/internal/platform/pg"
	"messagehub/internal/secrets"
)

// Exit codes. config check distinguishes a bad file (1) from missing
// environment variables (2) so CI and deploy scripts can tell them apart.
const (
	exitOK         = 0
	exitError      = 1
	exitMissingEnv = 2
	exitUsage      = 64
)

// migrateTimeout bounds one `migrate up` run.
const migrateTimeout = 10 * time.Minute

const usageText = `usage: messagehub <command> [flags]

commands:
  config check [--config PATH]   validate the config and check its environment variables
  config env   [--config PATH]   print every environment variable the config references
  migrate up                     apply database migrations (HUB_DATABASE_URL)
  keygen                         generate an inbound API key and its sha256
  serve [--role=api|worker|all]  run the hub; SIGHUP reloads the config

--config defaults to HUB_CONFIG_PATH.
`

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// cli carries the process environment and output streams, injected for tests.
type cli struct {
	lookup func(string) (string, bool)
	stdout io.Writer
	stderr io.Writer
}

func run(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	c := cli{lookup: lookup, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		return c.usageError("missing command")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(c.stdout, usageText)
		return exitOK
	case "config":
		return c.config(args[1:])
	case "migrate":
		return c.migrate(args[1:])
	case "keygen":
		return c.keygen(args[1:])
	case "serve":
		return c.serve(args[1:])
	default:
		return c.usageError(fmt.Sprintf("unknown command %q", args[0]))
	}
}

func (c cli) usageError(msg string) int {
	fmt.Fprintf(c.stderr, "error: %s\n\n%s", msg, usageText)
	return exitUsage
}

func (c cli) fail(code int, err error) int {
	fmt.Fprintf(c.stderr, "error: %v\n", err)
	return code
}

// parseFlags parses args into fs. ok is false when the command must stop
// and return code (help requested, bad flag or stray argument).
func (c cli) parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	fs.SetOutput(c.stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Sprintf("unexpected arguments %v", fs.Args())), false
	}
	return exitOK, true
}

func (c cli) config(args []string) int {
	if len(args) == 0 {
		return c.usageError("config needs a subcommand: check or env")
	}
	sub := args[0]
	if sub != "check" && sub != "env" {
		return c.usageError(fmt.Sprintf("unknown config subcommand %q", sub))
	}
	fs := flag.NewFlagSet("config "+sub, flag.ContinueOnError)
	defaultPath, _ := c.lookup(app.EnvConfigPath)
	path := fs.String("config", defaultPath, "config file path")
	if code, ok := c.parseFlags(fs, args[1:]); !ok {
		return code
	}
	if *path == "" {
		return c.usageError("--config or " + app.EnvConfigPath + " is required")
	}
	cfg, err := config.Load(*path, configOptions()...)
	if err != nil {
		return c.fail(exitError, err)
	}
	if sub == "env" {
		for _, name := range cfg.RequiredEnv() {
			fmt.Fprintln(c.stdout, name)
		}
		return exitOK
	}
	return c.configCheck(*path, cfg)
}

func (c cli) configCheck(path string, cfg *config.Config) int {
	fmt.Fprintf(c.stdout, "config ok: %s\nconfig_hash: %s\n", path, cfg.Hash())
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(c.stdout, "warning: %s\n", w)
	}
	fmt.Fprintf(c.stdout, "required environment variables: %d\n", len(cfg.RequiredEnv()))
	resolver := secrets.NewEnvResolver(c.lookup)
	err := secrets.CheckRequired(cfg, resolver)
	var missing *secrets.MissingError
	switch {
	case errors.As(err, &missing):
		return c.fail(exitMissingEnv, err)
	case err != nil:
		return c.fail(exitError, err)
	}
	if err := app.CheckPrincipalKeys(cfg, resolver); err != nil {
		return c.fail(exitError, err)
	}
	return exitOK
}

func (c cli) migrate(args []string) int {
	if len(args) == 0 || args[0] != "up" {
		return c.usageError("migrate needs the subcommand up")
	}
	fs := flag.NewFlagSet("migrate up", flag.ContinueOnError)
	if code, ok := c.parseFlags(fs, args[1:]); !ok {
		return code
	}
	dsn, _ := c.lookup(app.EnvDatabaseURL)
	if dsn == "" {
		return c.fail(exitError, errors.New(app.EnvDatabaseURL+" is required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	applied, err := pg.MigrateUp(ctx, dsn)
	if err != nil {
		return c.fail(exitError, err)
	}
	fmt.Fprintf(c.stdout, "applied %d migrations\n", applied)
	return exitOK
}

// keygen prints the key on stdout only; it is never logged.
func (c cli) keygen(args []string) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	if code, ok := c.parseFlags(fs, args); !ok {
		return code
	}
	key, hash, err := apikey.Generate(rand.Reader)
	if err != nil {
		return c.fail(exitError, err)
	}
	fmt.Fprintf(c.stdout, "key: %s\nsha256: %s\n", key, hash)
	fmt.Fprintln(c.stderr, "note: the key is shown only once; give it to the caller and put the sha256 in the principal's environment variable")
	return exitOK
}
