//go:build !darwin && !linux

package host

import "fmt"

func sessionProcessIDs(sid int) ([]int, error) {
	return nil, fmt.Errorf("process-session enumeration is unsupported")
}
