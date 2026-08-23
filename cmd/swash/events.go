package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"swa.sh/internal/backend"
	"swa.sh/internal/journal"
)

var queryFieldNamePattern = regexp.MustCompile(`^_?[A-Z][A-Z0-9_]*$`)

type printableEvent struct {
	Cursor    string            `json:"cursor"`
	Timestamp time.Time         `json:"timestamp"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields"`
}

func cmdEvents(args []string) {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetInterspersed(true)
	fs.SetOutput(os.Stderr)

	sessionID := fs.String("session", "", "Match SWASH_SESSION")
	event := fs.String("event", "", "Match SWASH_EVENT")
	fieldValues := fs.StringArrayP("field", "f", nil, "Match KEY=VALUE field (repeatable)")
	cursor := fs.String("cursor", "", "Return entries after this cursor")
	follow := fs.Bool("follow", false, "Continue waiting for matching events")
	last := fs.Int("last", 0, "Print only the last N existing matches")
	jsonOutput := fs.Bool("json", false, "Print one JSON object per event")
	all := fs.Bool("all", false, "Allow an unfiltered query")
	backendName := fs.String("backend", os.Getenv("SWASH_BACKEND"), "Backend: systemd, posix")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: swash events [--session ID] [--event NAME] [--field KEY=VALUE ...]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if len(fs.Args()) != 0 {
		fatal("events does not accept positional arguments")
	}
	if *last < 0 {
		fatal("--last must be non-negative")
	}

	filters, err := parseEventFilters(*fieldValues)
	if err != nil {
		fatal("%v", err)
	}
	if *sessionID != "" {
		filters = append(filters, backend.EventFilter{Field: journal.FieldSession, Value: *sessionID})
	}
	if *event != "" {
		filters = append(filters, backend.EventFilter{Field: journal.FieldEvent, Value: *event})
	}
	if len(filters) == 0 && !*all {
		fatal("events requires a filter; pass --all to query the entire journal")
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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	entries, newCursor, err := bk.PollEvents(ctx, filters, *cursor)
	if err != nil {
		fatal("querying events: %v", err)
	}
	if *last > 0 && len(entries) > *last {
		entries = entries[len(entries)-*last:]
	}
	for _, entry := range entries {
		if err := printEvent(entry, *jsonOutput); err != nil {
			fatal("printing event: %v", err)
		}
	}

	if !*follow {
		return
	}
	if newCursor == "" {
		newCursor = *cursor
	}
	for entry := range bk.FollowEvents(ctx, filters, newCursor) {
		if err := printEvent(entry, *jsonOutput); err != nil {
			fatal("printing event: %v", err)
		}
	}
}

func parseEventFilters(values []string) ([]backend.EventFilter, error) {
	filters := make([]backend.EventFilter, 0, len(values))
	for _, value := range values {
		key, fieldValue, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("field %q must be KEY=VALUE", value)
		}
		if !queryFieldNamePattern.MatchString(key) {
			return nil, fmt.Errorf("field name %q must be an uppercase journal field", key)
		}
		filters = append(filters, backend.EventFilter{Field: key, Value: fieldValue})
	}
	return filters, nil
}

func printEvent(entry journal.EventRecord, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(os.Stdout).Encode(printableEvent{
			Cursor:    entry.Cursor,
			Timestamp: entry.Timestamp,
			Message:   entry.Message,
			Fields:    entry.Fields,
		})
	}

	sessionID := entry.Fields[journal.FieldSession]
	event := entry.Fields[journal.FieldEvent]
	if sessionID == "" {
		sessionID = "-"
	}
	if event == "" {
		event = "-"
	}
	fmt.Printf("%s %-8s %-16s %s",
		entry.Timestamp.Format(time.RFC3339), sessionID, event, entry.Message)

	keys := make([]string, 0, len(entry.Fields))
	for key := range entry.Fields {
		switch key {
		case "MESSAGE", journal.FieldSession, journal.FieldEvent, "SWASH_WRITE_NONCE", "PRIORITY":
			continue
		}
		if strings.HasPrefix(key, "_") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf(" %s=%s", key, strconv.Quote(entry.Fields[key]))
	}
	fmt.Println()
	return nil
}
