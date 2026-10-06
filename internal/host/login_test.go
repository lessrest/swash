package host

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestForwardedEnvironmentDropsInvalidNames(t *testing.T) {
	got := ForwardedEnvironment([]string{
		"HOME=/home/u",
		"SENTRY-TRACE=abc",
		"_=/usr/bin/env",
		"9LIVES=no",
		"EMPTY=",
		"A_B9=x=y",
	})
	want := map[string]string{"HOME": "/home/u", "EMPTY": "", "A_B9": "x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ForwardedEnvironment = %v, want %v", got, want)
	}
}

func TestPasswdShell(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	data := "root:x:0:0::/root:/bin/sh\nu:x:1000:100::/home/u:/run/current-system/sw/bin/zsh\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := passwdShell(path, 1000); got != "/run/current-system/sw/bin/zsh" {
		t.Fatalf("passwdShell(1000) = %q", got)
	}
	if got := passwdShell(path, 1234); got != "" {
		t.Fatalf("passwdShell(1234) = %q, want empty", got)
	}
}

func TestLoginCommandPassesArgsThrough(t *testing.T) {
	got := LoginCommand([]string{"echo", "a b"})
	if got[1] != "-l" || got[2] != "-c" || got[3] != `exec "$@"` || !reflect.DeepEqual(got[5:], []string{"echo", "a b"}) {
		t.Fatalf("LoginCommand = %q", got)
	}
}
