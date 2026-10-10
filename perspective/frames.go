package perspective

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
)

// eachFrame calls work for every frame 0 to n-1, as many at a time as there
// are processors, and tells progress, when not nil, how many are done as
// each is -- one call at a time, in increasing order of done. It returns the
// first error any frame returned, or ctx's, once every frame started has
// finished; frames not yet started are not started after one.
//
// A flight's names are planned, and the tiles it sees found, a frame at a
// time from small pictures; each picture is filled across the processors,
// but drawn that small it is mostly the work around the filling -- the
// mesh, the vertices placed -- which is not, and planning a long flight one
// frame after another left most of the machine idle for many minutes. The
// frames are independent, so the caller keeps each one's result by its
// index and puts them together in order afterwards: the answer is the one
// the frames worked through in turn would have given.
func eachFrame(ctx context.Context, n int, work func(i int) error, progress func(done, total int)) error {
	if progress != nil {
		progress(0, n)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		next     atomic.Int64
		mu       sync.Mutex // guards done, firstErr and calls to progress
		done     int
		firstErr error
		wg       sync.WaitGroup
	)
	for range max(1, min(runtime.GOMAXPROCS(0), n)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n || ctx.Err() != nil {
					return
				}
				err := work(i)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				done++
				if progress != nil && firstErr == nil {
					progress(done, n)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return context.Cause(ctx)
}
