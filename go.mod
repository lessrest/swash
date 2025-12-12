module github.com/mbrock/swash

go 1.24.0

toolchain go1.24.4

require (
	github.com/a-h/templ v0.3.960
	github.com/coreos/go-systemd/v22 v22.6.0
	github.com/creack/pty v1.1.24
	github.com/godbus/dbus/v5 v5.2.0
	github.com/spf13/pflag v1.0.10
	golang.org/x/net v0.47.0
	golang.org/x/sys v0.38.0
	golang.org/x/term v0.37.0
)

require (
	github.com/Velocidex/go-journalctl v0.0.0-20250902002606-881a5f66df10 // indirect
	github.com/Velocidex/json v0.0.0-20220224052537-92f3c0326e5a // indirect
	github.com/Velocidex/ordereddict v0.0.0-20230909174157-2aa49cc5d11d // indirect
	github.com/Velocidex/yaml/v2 v2.2.8 // indirect
	github.com/dchest/siphash v1.2.3 // indirect
)

replace github.com/coreos/go-systemd/v22 => github.com/mbrock/go-systemd/v22 v22.6.1-swash
