package statedb

import "context"

type writeCheckpointKey struct{}

// WithValidatedWrite attaches a request-local admission check to supported
// owner writes. The store calls check inside its single writer transaction,
// after replay, resource, field and version validation and before the first
// state change. A successful idempotent replay also calls it before returning.
// The callback must be bounded and must not read the store or use the network.
// A nil check leaves internal callers unchanged.
func WithValidatedWrite(ctx context.Context, check func() error) context.Context {
	return context.WithValue(ctx, writeCheckpointKey{}, check)
}

func validatedWrite(ctx context.Context) error {
	check, _ := ctx.Value(writeCheckpointKey{}).(func() error)
	if check != nil {
		return check()
	}
	return nil
}
