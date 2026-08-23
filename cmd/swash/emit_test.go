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
