// Package adapter is the Telegram frontend of the engine: it turns each inbound
// update into one engine call and delivers the returned messages. It holds no
// conversation logic. Updates of one chat are handled strictly in arrival order
// by one serial worker; different chats run in parallel.
package adapter

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/valpere/v2v-bot/internal/engineclient"
	"github.com/valpere/v2v-bot/internal/telegram"
)

// Engine is the part of the engine client the adapter needs.
type Engine interface {
	Turn(ctx context.Context, chatID int64, ev engineclient.Event) (engineclient.Output, error)
}

const (
	chatQueueSize  = 64               // pending updates per chat before overflow is dropped
	slowDownWindow = 30 * time.Second // at most one "slow down" notice per chat per window
	slowDownLine   = "Забагато повідомлень одразу — зачекайте, будь ласка, поки я відповім на попередні. / Too many messages at once — please wait until I answer the earlier ones."

	// defaultIdleEvict is how long a chat's worker may sit idle before it is released.
	defaultIdleEvict = 15 * time.Minute
)

// Adapter wires a Telegram client to an engine.
type Adapter struct {
	tg  telegram.Client
	eng Engine

	mu        sync.Mutex
	inbox     map[int64]chan telegram.Update // per-chat FIFO queue (one serial worker each)
	idleEvict time.Duration                  // 0 = defaultIdleEvict
	slow      map[int64]time.Time            // last "slow down" notice per chat
	wg        sync.WaitGroup                 // outstanding chatWorker goroutines
}

// New returns an Adapter.
func New(tg telegram.Client, eng Engine) *Adapter {
	return &Adapter{tg: tg, eng: eng, inbox: make(map[int64]chan telegram.Update)}
}

// Run dispatches updates until the channel closes (ctx cancelled), then waits
// for the workers: one may still be mid-turn, and shutdown must not cut it off.
func (a *Adapter) Run(ctx context.Context, updates <-chan telegram.Update) {
	for u := range updates {
		a.dispatch(ctx, u)
	}
	a.wg.Wait()
}

// dispatch routes an update to its chat's serial worker, spawning the worker on
// first contact. This keeps one chat's turns in strict arrival order while
// different chats run concurrently.
func (a *Adapter) dispatch(ctx context.Context, u telegram.Update) {
	// The non-blocking send happens under a.mu so an idle worker cannot be
	// evicted between "found its queue" and "queued the update".
	a.mu.Lock()
	ch := a.inbox[u.ChatID]
	if ch == nil {
		ch = make(chan telegram.Update, chatQueueSize)
		a.inbox[u.ChatID] = ch
		a.wg.Add(1)
		go a.chatWorker(ctx, u.ChatID, ch)
	}
	queued := true
	select {
	case ch <- u:
	default:
		queued = false
	}
	notify := false
	if !queued {
		// Queue full: never block the single update loop (that would stall
		// every other chat). Drop the update; tell the sender once per window.
		notify = time.Since(a.slow[u.ChatID]) >= slowDownWindow
		if notify {
			if a.slow == nil {
				a.slow = make(map[int64]time.Time)
			}
			a.slow[u.ChatID] = time.Now()
		}
	}
	a.mu.Unlock()

	if queued {
		return
	}
	log.Printf("dispatch (chat %d): queue full, dropped an update", u.ChatID)
	if notify {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.send(ctx, u.ChatID, slowDownLine)
		}()
	}
}

// chatWorker serves one chat's queue until ctx ends or the chat has been idle
// for idleEvict, at which point it deregisters itself (a later message starts a
// fresh worker, so a long-running process does not hold a goroutine and a
// 64-slot channel per chat ever seen).
func (a *Adapter) chatWorker(ctx context.Context, chatID int64, ch chan telegram.Update) {
	defer a.wg.Done()
	idle := a.idleEvict
	if idle <= 0 {
		idle = defaultIdleEvict
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-ch:
			// an update already taken off the queue is finished even when shutdown
			// begins (the engine completes the turn regardless; dropping its reply
			// would lose it) — bounded by the engine client's timeout
			a.handleUpdate(context.WithoutCancel(ctx), u)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			a.mu.Lock()
			if len(ch) == 0 { // dispatch queues under a.mu, so this is exact
				delete(a.inbox, chatID)
				a.mu.Unlock()
				return
			}
			a.mu.Unlock()
			timer.Reset(idle)
		}
	}
}
