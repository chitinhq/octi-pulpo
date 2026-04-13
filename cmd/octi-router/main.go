// Command octi-router is the v0 CLI for the Cognitive Router (octi#196).
//
// Usage:
//
//	octi-router route --type debugging
//	octi-router route --type algorithmic --paths .github/workflows/ci.yml
//	octi-router route --type refactor --risk critical --id TASK-42
//
// Emits a JSON Decision on stdout and a flow.octi.router.route event.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chitinhq/octi-pulpo/internal/cogrouter"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "route":
		if err := runRoute(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `octi-router — Cognitive Router v0 (octi#196)

Usage:
  octi-router route [flags]

Flags:
  --config <path>   path to router.yaml (default: config/router.yaml)
  --id <id>         task id (optional)
  --type <type>     task type (debugging, algorithmic, architecture, ...)
  --risk <risk>     low | medium | high | critical
  --ambiguity <a>   low | medium | high
  --paths <p,p>     comma-separated touched paths
  --urgency <u>     urgency tag
`)
}

func runRoute(args []string) error {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to router.yaml")
	id := fs.String("id", "", "task id")
	typ := fs.String("type", "", "task type")
	risk := fs.String("risk", "", "risk level")
	ambiguity := fs.String("ambiguity", "", "ambiguity level")
	paths := fs.String("paths", "", "comma-separated touched paths")
	urgency := fs.String("urgency", "", "urgency")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := cogrouter.LoadRules(*cfgPath)
	if err != nil {
		return err
	}
	r, err := cogrouter.New(cfg)
	if err != nil {
		return err
	}

	ctx := cogrouter.TaskContext{
		ID:        *id,
		Type:      *typ,
		Risk:      *risk,
		Ambiguity: *ambiguity,
		Urgency:   *urgency,
	}
	if *paths != "" {
		for _, p := range strings.Split(*paths, ",") {
			if p = strings.TrimSpace(p); p != "" {
				ctx.TouchedPaths = append(ctx.TouchedPaths, p)
			}
		}
	}

	d, err := r.Route(ctx)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// defaultConfigPath looks for config/router.yaml relative to the current
// working directory. Absolute paths are preferred in scripts.
func defaultConfigPath() string {
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "config", "router.yaml")
	}
	return "config/router.yaml"
}
