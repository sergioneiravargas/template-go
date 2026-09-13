package amqpx_test

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/log"

	amqp "github.com/rabbitmq/amqp091-go"
)

func testLogger() *log.Logger {
	return log.NewLogger("test", log.NewHandler(os.Stdout, "dev"))
}

// fastConfig keeps backoff/window tiny so tests run in milliseconds.
func fastConfig() amqpx.Config {
	return amqpx.Config{
		MinBackoff:         time.Millisecond,
		MaxBackoff:         2 * time.Millisecond,
		MaxReconnectWindow: 50 * time.Millisecond,
	}
}

// fakeSession simulates a broker connection without a live broker.
type fakeSession struct {
	mu           sync.Mutex
	closed       bool
	notify       chan *amqp.Error
	channelCalls int
}

func (f *fakeSession) Channel() (*amqp.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channelCalls++
	// A nil channel is fine for tests: topology callbacks under test don't
	// dereference it, and applyTopology guards the nil before Close().
	return nil, nil
}

func (f *fakeSession) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	f.mu.Lock()
	f.notify = c
	f.mu.Unlock()
	return c
}

func (f *fakeSession) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		if f.notify != nil {
			close(f.notify)
		}
	}
	return nil
}

// drop simulates the broker dropping the connection unexpectedly.
func (f *fakeSession) drop() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	n := f.notify
	f.mu.Unlock()
	if n != nil {
		n <- &amqp.Error{Code: 504, Reason: "channel/connection is not open"}
	}
}

func (f *fakeSession) channelCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channelCalls
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition not met before timeout")
		case <-tick.C:
		}
	}
}

func TestNewReturnsErrorWhenInitialDialFails(t *testing.T) {
	_, err := amqpx.New(fastConfig(), func() (amqpx.Session, error) {
		return nil, errors.New("broker down")
	}, testLogger())
	if err == nil {
		t.Fatal("expected error when initial dial fails")
	}
}

func TestReconnectsAfterConnectionDrop(t *testing.T) {
	var mu sync.Mutex
	var sessions []*fakeSession
	dialer := func() (amqpx.Session, error) {
		s := &fakeSession{}
		mu.Lock()
		sessions = append(sessions, s)
		mu.Unlock()
		return s, nil
	}

	m, err := amqpx.New(fastConfig(), dialer, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer m.Close()

	mu.Lock()
	first := sessions[0]
	mu.Unlock()
	first.drop()

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sessions) >= 2
	})

	if !m.IsConnected() {
		t.Fatal("expected manager to be connected after reconnect")
	}
}

func TestGivesUpAfterReconnectWindowExhausted(t *testing.T) {
	var mu sync.Mutex
	dialCount := 0
	first := &fakeSession{}
	dialer := func() (amqpx.Session, error) {
		mu.Lock()
		dialCount++
		c := dialCount
		mu.Unlock()
		if c == 1 {
			return first, nil // initial connect succeeds
		}
		return nil, errors.New("broker still down") // every reconnect fails
	}

	gaveUp := make(chan struct{})
	m, err := amqpx.New(fastConfig(), dialer, testLogger(), amqpx.WithOnGiveUp(func() {
		close(gaveUp)
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer m.Close()

	first.drop()

	select {
	case <-gaveUp:
	case <-time.After(2 * time.Second):
		t.Fatal("expected give-up callback after reconnect window exhausted")
	}
}

func TestTopologyReappliedOnReconnect(t *testing.T) {
	var mu sync.Mutex
	var sessions []*fakeSession
	dialer := func() (amqpx.Session, error) {
		s := &fakeSession{}
		mu.Lock()
		sessions = append(sessions, s)
		mu.Unlock()
		return s, nil
	}

	m, err := amqpx.New(fastConfig(), dialer, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer m.Close()

	var applied int
	if err := m.RegisterTopology(func(*amqp.Channel) error {
		mu.Lock()
		applied++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("unexpected error registering topology: %v", err)
	}

	// Applied once immediately on registration.
	mu.Lock()
	if applied != 1 {
		mu.Unlock()
		t.Fatalf("expected topology applied once on register, got %d", applied)
	}
	first := sessions[0]
	mu.Unlock()

	first.drop()

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return applied >= 2
	})
}

func TestChannelReturnsErrNotConnectedAfterClose(t *testing.T) {
	s := &fakeSession{}
	m, err := amqpx.New(fastConfig(), func() (amqpx.Session, error) {
		return s, nil
	}, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if _, err := m.Channel(); !errors.Is(err, amqpx.ErrNotConnected) {
		t.Fatalf("expected ErrNotConnected after close, got %v", err)
	}
}

func TestCloseStopsReconnect(t *testing.T) {
	var mu sync.Mutex
	first := &fakeSession{}
	dialCount := 0
	dialer := func() (amqpx.Session, error) {
		mu.Lock()
		dialCount++
		c := dialCount
		mu.Unlock()
		if c == 1 {
			return first, nil
		}
		return nil, errors.New("should not be dialed after close")
	}

	m, err := amqpx.New(fastConfig(), dialer, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Close before dropping: the close-notify from Close() must not trigger a
	// reconnect.
	if err := m.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if dialCount != 1 {
		t.Fatalf("expected no reconnect dials after close, got %d dials", dialCount)
	}
}
