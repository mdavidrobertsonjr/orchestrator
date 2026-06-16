package scheduler

import "context"

type Lock interface {
	TryLock(ctx context.Context) (func(), bool, error)
}

type noopLock struct{}

func (noopLock) TryLock(ctx context.Context) (func(), bool, error) {
	return func() {}, true, nil
}
