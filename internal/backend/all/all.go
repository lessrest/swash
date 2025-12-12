// Package all registers all built-in swash backends.
//
// Import for side effects:
//
//	import _ "github.com/mbrock/swash/internal/backend/all"
package all

import (
	_ "github.com/mbrock/swash/internal/backend/posix"
	_ "github.com/mbrock/swash/internal/backend/systemd"
)
