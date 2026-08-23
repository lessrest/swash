package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	flag "github.com/spf13/pflag"

	"swa.sh/internal/backend"
	"swa.sh/internal/journal"
)

var eventNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var fieldNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func cmdEmit(args []string) {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetInterspersed(true)
	fs.SetOutput(os.Stderr)

	event := fs.String("event", "", "Semantic event name")
	message := fs.String("message", "", "Human-readable event message (defaults to event name)")
	fields := fs.StringArrayP("field", "f", nil, "Structured KEY=VALUE field (repeatable)")
	backendName := fs.String("backend", os.Getenv("SWASH_BACKEND"), "Backend: systemd, posix")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: swash emit SESSION --event NAME [--message TEXT] [--field KEY=VALUE ...]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	positional := fs.Args()
	if len(positional) != 1 || *event == "" {
		fs.Usage()
		os.Exit(2)
	}
	if !eventNamePattern.MatchString(*event) {
		fatal("event name %q must contain only lowercase letters, digits, and hyphens", *event)
	}

	parsedFields, err := parseEventFields(*fields)
	if err != nil {
		fatal("%v", err)
	}
	if *message == "" {
		*message = *event
	}

	kind := backend.Kind(*backendName)
	if kind == "" {
		kind = backend.DetectKind()
	}
	bk, err := backend.Open(context.Background(), backend.Config{
		Kind:        kind,
		HostCommand: findHostCommand(),
	})
	if err != nil {
		fatal("initializing backend: %v", err)
	}
	defer bk.Close()

	sessionID := positional[0]
	if err := bk.EmitSessionEvent(context.Background(), sessionID, *event, *message, parsedFields); err != nil {
		fatal("emitting event: %v", err)
	}
	fmt.Printf("%s %s\n", sessionID, *event)
}

func parseEventFields(values []string) (map[string]string, error) {
	fields := make(map[string]string, len(values))
	for _, value := range values {
		key, fieldValue, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("field %q must be KEY=VALUE", value)
		}
		if !fieldNamePattern.MatchString(key) {
			return nil, fmt.Errorf("field name %q must contain only uppercase letters, digits, and underscores", key)
		}
		switch key {
		case journal.FieldSession, journal.FieldEvent, "MESSAGE", "FD":
			return nil, fmt.Errorf("field name %q is reserved", key)
		}
		fields[key] = fieldValue
	}
	return fields, nil
}
