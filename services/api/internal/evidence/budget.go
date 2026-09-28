package evidence

import (
	"context"
	"sync/atomic"

	"golang.org/x/sync/semaphore"
)

// decodeBudget bounds the decoder memory of all concurrent jobs in the
// process. Each decode reserves its whole estimate in one call, so no job
// holds part of the budget while waiting for more.
type decodeBudget struct {
	sem   *semaphore.Weighted
	size  int64
	inUse atomic.Int64
	waits atomic.Uint64
}

func newDecodeBudget(size int64) *decodeBudget {
	return &decodeBudget{sem: semaphore.NewWeighted(size), size: size}
}

// acquire reserves weight bytes, waiting at most until ctx ends. A nil budget
// is unbounded, which suits single-shot callers such as Validate.
func (b *decodeBudget) acquire(ctx context.Context, weight int64) (release func(), err error) {
	if b == nil {
		return func() {}, nil
	}
	if weight > b.size {
		return nil, errDecodeCapacity
	}
	if !b.sem.TryAcquire(weight) {
		b.waits.Add(1)
		if err := b.sem.Acquire(ctx, weight); err != nil {
			return nil, errDecodeCapacity
		}
	}
	b.inUse.Add(weight)
	return func() {
		b.inUse.Add(-weight)
		b.sem.Release(weight)
	}, nil
}
