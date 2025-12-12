module github.com/mbrock/swash

go 1.25.0

toolchain go1.25.5

require (
	github.com/a-h/templ v0.3.960
	github.com/coreos/go-systemd/v22 v22.6.0
	github.com/creack/pty v1.1.24
	github.com/dchest/siphash v1.2.3
	github.com/godbus/dbus/v5 v5.2.0
	github.com/klauspost/compress v1.18.2
	github.com/spf13/pflag v1.0.10
	github.com/tetratelabs/wazero v1.10.1
	golang.org/x/net v0.47.0
	golang.org/x/sys v0.38.0
	golang.org/x/term v0.37.0
)

require github.com/fsnotify/fsnotify v1.9.0

replace github.com/coreos/go-systemd/v22 => github.com/mbrock/go-systemd/v22 v22.6.1-swash
