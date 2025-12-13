// Package all registers all built-in swash backends.
//
// Import for side effects:
//
//	import _ "swa.sh/internal/backend/all"
package all

import (
	_ "swa.sh/internal/backend/posix"
	_ "swa.sh/internal/backend/systemd"
)
