package provider

import "sync"

// CaptureConcurrency bounds parallel terminal reads per poll, so one wedged
// bridge costs a tick its capture timeout once rather than once per session.
const CaptureConcurrency = 8

// Fanout calls call for every index below count, at most limit at a time, and
// returns when all calls have.
func Fanout(count, limit int, call func(int)) {
	slots := make(chan struct{}, max(limit, 1))
	var wg sync.WaitGroup
	for i := range count {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			call(i)
		}()
	}
	wg.Wait()
}
