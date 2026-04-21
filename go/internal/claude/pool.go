package claude

import (
	"context"
	"log/slog"
	"sync"
)

// Class selects which queue a Pool job uses.
type Class int

const (
	// ClassMain jobs run on the single worker pinned to the main session so
	// turns that touch main are strictly FIFO and never interleave.
	ClassMain Class = iota
	// ClassEphemeral jobs run on one of the shared workers — new/dispatch/
	// callback work that doesn't need a dedicated slot.
	ClassEphemeral
)

func (c Class) String() string {
	if c == ClassMain {
		return "main"
	}
	return "ephemeral"
}

// DefaultEphemeralWorkers is the default number of workers serving the
// ephemeral queue.
const DefaultEphemeralWorkers = 5

// DefaultQueueCapacity is the buffered size of each queue. Hitting the cap
// causes Submit to block (backpressure), which is preferable to unbounded
// memory growth.
const DefaultQueueCapacity = 1024

type job struct {
	ctx      context.Context
	opts     Options
	resultCh chan jobResult
}

type jobResult struct {
	res *Result
	err error
}

// Pool owns a shared Runner and a set of workers: one pinned to main, plus N
// workers draining a shared ephemeral queue. The Runner itself is stateless
// — the pool adds queuing and concurrency control around it.
type Pool struct {
	runner     *Runner
	mainQ      chan *job
	ephemeralQ chan *job
	wg         sync.WaitGroup
}

// NewPool starts one main worker and `ephemeral` ephemeral workers. Queues are
// buffered with `queueCap` slots; pass 0 for DefaultQueueCapacity, and 0 for
// `ephemeral` to use DefaultEphemeralWorkers.
func NewPool(runner *Runner, ephemeral, queueCap int) *Pool {
	if ephemeral <= 0 {
		ephemeral = DefaultEphemeralWorkers
	}
	if queueCap <= 0 {
		queueCap = DefaultQueueCapacity
	}
	p := &Pool{
		runner:     runner,
		mainQ:      make(chan *job, queueCap),
		ephemeralQ: make(chan *job, queueCap),
	}
	p.wg.Add(1)
	go p.worker(ClassMain, 0, p.mainQ)
	for i := 0; i < ephemeral; i++ {
		p.wg.Add(1)
		go p.worker(ClassEphemeral, i, p.ephemeralQ)
	}
	slog.Info("claude pool started", "main_workers", 1, "ephemeral_workers", ephemeral, "queue_cap", queueCap)
	return p
}

func (p *Pool) worker(class Class, id int, q <-chan *job) {
	defer p.wg.Done()
	for j := range q {
		if j.ctx.Err() != nil {
			j.resultCh <- jobResult{nil, j.ctx.Err()}
			continue
		}
		slog.Debug("pool worker picked job", "class", class.String(), "worker", id)
		res, err := p.runner.Run(j.ctx, j.opts)
		j.resultCh <- jobResult{res, err}
	}
}

// Run enqueues a job on the queue selected by `class` and blocks until the
// worker returns a result or ctx is cancelled.
//
// Note: if ctx is cancelled while the job is still queued, Run returns the
// cancellation immediately — but the job slot stays on the queue and a
// worker will eventually pick it up, see the cancelled context, and drop
// it. This keeps queue ordering simple and costs at most one no-op worker
// cycle per cancelled submission.
func (p *Pool) Run(ctx context.Context, class Class, opts Options) (*Result, error) {
	q := p.ephemeralQ
	if class == ClassMain {
		q = p.mainQ
	}
	j := &job{ctx: ctx, opts: opts, resultCh: make(chan jobResult, 1)}
	select {
	case q <- j:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-j.resultCh:
		return r.res, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// QueueDepth returns the current number of queued (not yet picked up) jobs
// for the given class. Useful for observability.
func (p *Pool) QueueDepth(class Class) int {
	if class == ClassMain {
		return len(p.mainQ)
	}
	return len(p.ephemeralQ)
}

// Close stops the pool after all queued and in-flight jobs finish. Callers
// must stop submitting before calling Close.
func (p *Pool) Close() {
	close(p.mainQ)
	close(p.ephemeralQ)
	p.wg.Wait()
}
