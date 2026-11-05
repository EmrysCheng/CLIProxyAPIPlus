// Package lifecycle tracks startup-owned and request-owned background work.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type key struct{}
type Group struct {
	mu      sync.Mutex
	active  int
	once    map[any]bool
	closing bool
	done    chan struct{}
	errors  chan error
	cancel  context.CancelFunc
}

func New(parent context.Context) (context.Context, *Group) {
	ctx, cancel := context.WithCancel(parent)
	g := &Group{done: make(chan struct{}), errors: make(chan error, 1), cancel: cancel}
	return context.WithValue(ctx, key{}, g), g
}
func (g *Group) Attach(ctx context.Context) context.Context { return context.WithValue(ctx, key{}, g) }
func (g *Group) Errors() <-chan error                       { return g.errors }
func Go(ctx context.Context, fn func()) bool {
	g, _ := ctx.Value(key{}).(*Group)
	if g == nil {
		go fn()
		return true
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return false
	}
	g.active++
	g.mu.Unlock()
	g.launch(fn)
	return true
}
func (g *Group) launch(fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				select {
				case g.errors <- fmt.Errorf("background task panicked: %v", p):
				default:
				}
				g.cancel()
			}
			g.mu.Lock()
			g.active--
			if g.closing && g.active == 0 {
				close(g.done)
			}
			g.mu.Unlock()
		}()
		fn()
	}()
}
func (g *Group) Wait(ctx context.Context) error {
	g.cancel()
	g.mu.Lock()
	if !g.closing {
		g.closing = true
		if g.active == 0 {
			close(g.done)
		}
	}
	g.mu.Unlock()
	select {
	case <-g.done:
		select {
		case err := <-g.errors:
			return err
		default:
			return nil
		}
	case <-ctx.Done():
		return errors.Join(errors.New("background drain deadline exceeded; database must remain open"), ctx.Err())
	}
}

func GoOnce(ctx context.Context, identity any, fn func()) bool {
	g, _ := ctx.Value(key{}).(*Group)
	if g == nil {
		go fn()
		return true
	}
	g.mu.Lock()
	if g.once == nil {
		g.once = make(map[any]bool)
	}
	if g.once[identity] || g.closing {
		g.mu.Unlock()
		return false
	}
	g.once[identity] = true
	g.active++
	g.mu.Unlock()
	g.launch(fn)
	return true
}

func (g *Group) Cancel()       { g.cancel() }
func (g *Group) Drained() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.closing && g.active == 0 }
