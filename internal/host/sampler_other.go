//go:build !linux

package host

import (
	"context"
	"time"
)

// Sampler is a stub on platforms without /proc and cgroups.
type Sampler struct{ start time.Time }

func NewSampler() *Sampler                 { return &Sampler{start: time.Now()} }
func (s *Sampler) SetTaskPID(int)          {}
func (s *Sampler) Run(ctx context.Context) { <-ctx.Done() }
func (s *Sampler) Latest() Stats           { return s.Sample() }
func (s *Sampler) Sample() Stats {
	now := time.Now()
	return Stats{Time: now, Elapsed: now.Sub(s.start).Seconds()}
}
