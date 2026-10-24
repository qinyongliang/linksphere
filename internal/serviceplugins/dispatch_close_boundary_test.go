package serviceplugins

import (
	"context"
	"sync/atomic"
	"testing"
)

type closingIdentityProvider struct {
	*diagProvider
	calls  atomic.Int32
	at     int32
	during func()
}

func (p *closingIdentityProvider) Identity() DispatchProviderIdentity {
	identity := p.diagProvider.Identity()
	if p.calls.Add(1) == p.at {
		p.during()
	}
	return identity
}

func TestDispatcherRechecksCloseAfterFinalIdentity(t *testing.T) {
	_, d, p, _ := eventFixture(t, 2)
	d.provider = &closingIdentityProvider{diagProvider: p, at: 2, during: d.Close}
	if err := d.CheckEvents(diagContext("actor-1"), diagBinding(), "call"); err == nil {
		t.Fatal("closed dispatcher admitted after final identity callback")
	}
}

func TestDispatcherRechecksCallerCancellationAfterFinalIdentity(t *testing.T) {
	_, d, p, _ := eventFixture(t, 2)
	ctx, cancel := context.WithCancel(diagContext("actor-1"))
	defer cancel()
	d.provider = &closingIdentityProvider{diagProvider: p, at: 2, during: cancel}
	if err := d.CheckEvents(ctx, diagBinding(), "call"); err == nil {
		t.Fatal("cancelled caller admitted after final identity callback")
	}
}

func TestEventPageDiscardedWhenFinalIdentityClosesDispatcher(t *testing.T) {
	b, d, p, _ := eventFixture(t, 2)
	if err := b.Append(diagBinding(), observedEvent()); err != nil {
		t.Fatal(err)
	}
	d.provider = &closingIdentityProvider{diagProvider: p, at: 4, during: d.Close}
	page, err := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2)
	if err == nil || len(page.Events) != 0 || page.Next.StreamID != "" || page.Next.Sequence != 0 || page.Lost != 0 {
		t.Fatalf("closed read returned copied data: %+v, %v", page, err)
	}
}
