package ring

import (
	"context"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// nativeWrite keeps only native journal jobs out of one another's unsettled
// store batch. It never holds a SQL transaction or a lock across caller I/O.
// A canceled caller leaves one DB-owned waiter to release the slot after the
// queued job's terminal outcome; the captured result must not be read then.
func (d *DB) nativeWrite(ctx context.Context, apply store.ApplyFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.nativeClosed:
		return store.ErrClosed
	case <-d.nativeSlot:
	}
	release := func() { d.nativeSlot <- struct{}{} }
	if err := ctx.Err(); err != nil {
		release()
		return err
	}
	select {
	case <-d.nativeClosed:
		release()
		return store.ErrClosed
	default:
	}
	done, err := d.s.Submit(ctx, apply)
	if err != nil {
		release()
		return err
	}
	select {
	case err = <-done:
		release()
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return err
	case <-ctx.Done():
		go func() {
			<-done
			release()
		}()
		return ctx.Err()
	}
}
