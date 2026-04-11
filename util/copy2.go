package util

import (
	"context"
	"errors"
	"io"
	"sync"
)

func Copy2(ctx context.Context, a, b io.ReadWriteCloser) (recv, sent int64, resErr error) {
	ch1, ch2 := make(chan struct{}), make(chan struct{})

	var setErr, closeA, closeB sync.Once

	go func() {
		defer close(ch1)
		bytes, err := io.Copy(a, b)
		sent += bytes
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			setErr.Do(func() {
				resErr = err
			})
		}
	}()

	go func() {
		defer close(ch2)
		bytes, err := io.Copy(b, a)
		recv += bytes
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			setErr.Do(func() {
				resErr = err
			})
		}
	}()

	ctxDone := ctx.Done()

	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			setErr.Do(func() {
				resErr = ctx.Err()
			})
			closeA.Do(func() {
				_ = a.Close()
			})
			closeB.Do(func() {
				_ = b.Close()
			})

		case <-ch1:
			ch1 = nil
			closeB.Do(func() {
				_ = a.Close()
			})

		case <-ch2:
			ch2 = nil
			closeA.Do(func() {
				_ = b.Close()
			})
		}

		if ch1 == nil && ch2 == nil {
			break
		}
	}

	return
}
