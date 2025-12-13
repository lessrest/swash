module swa.sh

go 1.25.0

toolchain go1.25.5

require (
	github.com/a-h/templ v0.3.960
	github.com/creack/pty v1.1.24
	github.com/dchest/siphash v1.2.3
	github.com/godbus/dbus/v5 v5.2.0
	github.com/klauspost/compress v1.18.2
	github.com/spf13/pflag v1.0.10
	github.com/tetratelabs/wazero v1.10.1
	golang.org/x/net v0.48.0
	golang.org/x/sys v0.39.0
	golang.org/x/term v0.38.0
)

require (
	github.com/PuerkitoBio/goquery v1.11.0
	github.com/fsnotify/fsnotify v1.9.0
	swa.sh/systemd v0.0.0-20251213214309-4327974344fb
	swa.sh/vterm v0.0.0-20251213213151-d532cc6939a9
)

require github.com/andybalholm/cascadia v1.3.3 // indirect
