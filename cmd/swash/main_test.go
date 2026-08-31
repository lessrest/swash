package main

import (
	"testing"
	"time"
)

func TestShouldAttachTTY(t *testing.T) {
	tests := []struct {
		name           string
		tty            bool
		detachAfter    time.Duration
		detachAfterSet bool
		want           bool
	}{
		{name: "default TTY run attaches", tty: true, detachAfter: 3 * time.Second, want: true},
		{name: "explicit immediate detach", tty: true, detachAfterSet: true, want: false},
		{name: "explicit nonzero delay still attaches", tty: true, detachAfter: time.Second, detachAfterSet: true, want: true},
		{name: "non-TTY run does not attach", detachAfter: 3 * time.Second, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAttachTTY(tt.tty, tt.detachAfter, tt.detachAfterSet); got != tt.want {
				t.Fatalf("shouldAttachTTY() = %v, want %v", got, tt.want)
			}
		})
	}
}
