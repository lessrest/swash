// Package all registers all built-in swash backends.
//
// Import for side effects:
//
//	import _ "swa.sh/go/swash/internal/backend/all"
package all

import (
	_ "swa.sh/go/swash/internal/backend/posix"
	_ "swa.sh/go/swash/internal/backend/systemd"
)
