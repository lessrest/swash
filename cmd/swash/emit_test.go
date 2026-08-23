package main

import "testing"

func TestParseEventFields(t *testing.T) {
	fields, err := parseEventFields([]string{
		"LUV_ROOT=/work/luv",
		"LUV_SLYNK_PORT=4172",
		"EMPTY=",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fields["LUV_ROOT"] != "/work/luv" || fields["LUV_SLYNK_PORT"] != "4172" {
		t.Fatalf("unexpected fields: %#v", fields)
	}
	if value, ok := fields["EMPTY"]; !ok || value != "" {
		t.Fatalf("empty value was not preserved: %#v", fields)
	}
}

func TestParseEventFiltersAllowsReservedQueryFields(t *testing.T) {
	filters, err := parseEventFilters([]string{
		"SWASH_SESSION=LISP01",
		"SWASH_EVENT=slynk-ready",
		"_PID=123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 3 || filters[0].Value != "LISP01" || filters[1].Value != "slynk-ready" || filters[2].Value != "123" {
		t.Fatalf("unexpected filters: %#v", filters)
	}
}

func TestParseEventFiltersRejectsMalformedFields(t *testing.T) {
	for _, value := range []string{"lower=value", "MISSING_EQUALS"} {
		if _, err := parseEventFilters([]string{value}); err == nil {
			t.Fatalf("parseEventFilters(%q) succeeded", value)
		}
	}
}

func TestParseEventFieldsRejectsReservedAndMalformedNames(t *testing.T) {
	for _, value := range []string{
		"SWASH_SESSION=wrong",
		"SWASH_EVENT=wrong",
		"MESSAGE=wrong",
		"FD=1",
		"lower=value",
		"MISSING_EQUALS",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseEventFields([]string{value}); err == nil {
				t.Fatalf("parseEventFields(%q) succeeded", value)
			}
		})
	}
}
