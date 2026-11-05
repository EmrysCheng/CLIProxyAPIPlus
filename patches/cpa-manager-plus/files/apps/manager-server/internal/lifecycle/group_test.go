package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGroupDrainsAndFences(t *testing.T) {
	ctx, g := New(context.Background())
	started := make(chan struct{})
	exited := make(chan struct{})
	Go(ctx, func() { close(started); <-ctx.Done(); close(exited) })
	<-started
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.Wait(stop); err != nil {
		t.Fatal(err)
	}
	<-exited
	if Go(ctx, func() { t.Error("accepted late task") }) {
		t.Fatal("accepted late task")
	}
}
func TestPanicPropagates(t *testing.T) {
	ctx, g := New(context.Background())
	Go(ctx, func() { panic("fixture") })
	<-ctx.Done()
	if err := g.Wait(context.Background()); err == nil {
		t.Fatal("panic was lost")
	}
}
func TestTimeoutDoesNotPretendToDrain(t *testing.T) {
	ctx, g := New(context.Background())
	release := make(chan struct{})
	Go(ctx, func() { <-release })
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	if err := g.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestGoOnce(t *testing.T) {
	ctx, g := New(context.Background())
	identity := new(int)
	release := make(chan struct{})
	if !GoOnce(ctx, identity, func() { <-release }) {
		t.Fatal("first rejected")
	}
	if GoOnce(ctx, identity, func() {}) {
		t.Fatal("duplicate started")
	}
	close(release)
	if err := g.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
