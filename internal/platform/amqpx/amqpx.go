// Package amqpx provides a self-healing wrapper around a RabbitMQ connection.
//
// The rabbitmq/amqp091-go driver does not reconnect on its own: once the
// underlying TCP connection drops (broker restart, network blip, missed
// heartbeats, or a correlated infrastructure outage), the *amqp.Connection is
// permanently closed and every subsequent Channel() call fails forever with
// "channel/connection is not open". ConnectionManager owns the connection,
// watches it for closure, and transparently re-dials with exponential backoff,
// re-declaring registered topology on every reconnect. If it cannot recover
// within a bounded window it invokes an "give up" callback so the process can
// fail fast and be recycled by the orchestrator (docker restart policy).
package amqpx

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ErrNotConnected is returned by Channel when there is no live connection to
// the broker (typically while a reconnect is in progress).
var ErrNotConnected = errors.New("amqp: not connected")

// Conn is the minimal channel-opening surface consumers depend on. Both
// *amqp.Connection and *ConnectionManager satisfy it, so callers can be written
// against the interface and tested without a live broker.
type Conn interface {
	Channel() (*amqp.Channel, error)
}

// TopologyRegistrar is implemented by connection managers that can re-run
// topology declarations (exchanges, queues, bindings) after a reconnect.
// Consumers register their declarations so they survive broker restarts, which
// is essential for non-durable queues that do not persist across a restart.
type TopologyRegistrar interface {
	RegisterTopology(setup func(*amqp.Channel) error) error
}

// Session is the subset of *amqp.Connection the manager relies on. Defined as
// an interface so tests can inject a fake without a live broker.
type Session interface {
	Channel() (*amqp.Channel, error)
	NotifyClose(chan *amqp.Error) chan *amqp.Error
	IsClosed() bool
	Close() error
}

// Dialer opens a new broker session. Swapped for a fake in tests.
type Dialer func() (Session, error)

// Config holds connection and reconnection tuning. Zero values fall back to
// production-sane defaults via withDefaults.
type Config struct {
	URL string

	// Heartbeat is the AMQP heartbeat interval; missed heartbeats surface a
	// dropped connection quickly instead of hanging.
	Heartbeat time.Duration
	// DialTimeout bounds each TCP dial attempt.
	DialTimeout time.Duration

	// MinBackoff / MaxBackoff bound the exponential backoff between reconnect
	// attempts.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// MaxReconnectWindow is how long the manager keeps trying to reconnect
	// before giving up and invoking the give-up callback. Reset on every
	// successful (re)connection.
	MaxReconnectWindow time.Duration
}

func (c Config) withDefaults() Config {
	if c.Heartbeat <= 0 {
		c.Heartbeat = 10 * time.Second
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 30 * time.Second
	}
	if c.MinBackoff <= 0 {
		c.MinBackoff = 1 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 30 * time.Second
	}
	if c.MaxReconnectWindow <= 0 {
		c.MaxReconnectWindow = 2 * time.Minute
	}
	return c
}

// ConnectionManager owns a broker connection and keeps it alive across drops.
type ConnectionManager struct {
	cfg      Config
	dialer   Dialer
	logger   *log.Logger
	onGiveUp func()

	mu         sync.RWMutex
	session    Session
	topologies []func(*amqp.Channel) error

	closeOnce sync.Once
	closed    chan struct{}
}

// Option customizes a ConnectionManager.
type Option func(*ConnectionManager)

// WithOnGiveUp sets a callback invoked when reconnection fails for longer than
// MaxReconnectWindow. Wire this to fx.Shutdowner(fx.ExitCode(1)) so the process
// exits and the container is recycled with a fresh connection.
func WithOnGiveUp(fn func()) Option {
	return func(m *ConnectionManager) { m.onGiveUp = fn }
}

// New dials the broker once and returns a manager watching the connection for
// drops. It returns an error if the initial dial fails (the caller typically
// panics at boot; the restart policy will retry until the broker is up).
func New(cfg Config, dialer Dialer, logger *log.Logger, opts ...Option) (*ConnectionManager, error) {
	if dialer == nil {
		panic("amqpx: dialer is required")
	}
	if logger == nil {
		panic("amqpx: logger is required")
	}

	m := &ConnectionManager{
		cfg:    cfg.withDefaults(),
		dialer: dialer,
		logger: logger,
		closed: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}

	session, err := dialer()
	if err != nil {
		return nil, fmt.Errorf("amqp: initial dial failed: %w", err)
	}
	m.session = session
	m.watch(session)
	return m, nil
}

// NewAMQPDialer returns a Dialer that opens a real broker connection with the
// configured heartbeat and dial timeout.
func NewAMQPDialer(cfg Config) Dialer {
	cfg = cfg.withDefaults()
	return func() (Session, error) {
		conn, err := amqp.DialConfig(cfg.URL, amqp.Config{
			Heartbeat: cfg.Heartbeat,
			Dial:      amqp.DefaultDial(cfg.DialTimeout),
		})
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

// RegisterTopology records a topology declaration and applies it immediately
// against the current connection. The declaration is re-applied on every
// reconnect. It must be idempotent (AMQP declarations are).
func (m *ConnectionManager) RegisterTopology(setup func(*amqp.Channel) error) error {
	m.mu.Lock()
	m.topologies = append(m.topologies, setup)
	session := m.session
	m.mu.Unlock()

	return applyTopology(session, setup)
}

// Channel opens a channel on the current live connection. It returns
// ErrNotConnected while a reconnect is in progress so callers surface a
// transient error instead of using a dead connection.
func (m *ConnectionManager) Channel() (*amqp.Channel, error) {
	m.mu.RLock()
	session := m.session
	m.mu.RUnlock()

	if session == nil || session.IsClosed() {
		return nil, ErrNotConnected
	}
	return session.Channel()
}

// IsConnected reports whether there is currently a live connection.
func (m *ConnectionManager) IsConnected() bool {
	m.mu.RLock()
	session := m.session
	m.mu.RUnlock()
	return session != nil && !session.IsClosed()
}

// Close stops reconnection and closes the current connection. Idempotent.
func (m *ConnectionManager) Close() error {
	m.closeOnce.Do(func() { close(m.closed) })

	m.mu.RLock()
	session := m.session
	m.mu.RUnlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

// watch subscribes to the session's close notification and triggers a
// reconnect when the connection drops unexpectedly.
func (m *ConnectionManager) watch(session Session) {
	notify := session.NotifyClose(make(chan *amqp.Error, 1))
	go func() {
		amqpErr := <-notify

		select {
		case <-m.closed:
			// Intentional shutdown via Close; do not reconnect.
			return
		default:
		}

		if amqpErr != nil {
			m.logger.Warn("AMQP connection lost, starting reconnect", log.Context{
				"error": amqpErr.Error(),
			})
		} else {
			m.logger.Warn("AMQP connection closed, starting reconnect", nil)
		}
		m.reconnect()
	}()
}

// reconnect re-dials with exponential backoff until it succeeds, the manager is
// closed, or the reconnect window is exhausted (which triggers give-up).
func (m *ConnectionManager) reconnect() {
	deadline := time.Now().Add(m.cfg.MaxReconnectWindow)

	for attempt := 1; ; attempt++ {
		select {
		case <-m.closed:
			return
		default:
		}

		session, err := m.dialer()
		if err == nil {
			if terr := m.applyAllTopologies(session); terr != nil {
				_ = session.Close()
				err = terr
			} else {
				m.mu.Lock()
				m.session = session
				m.mu.Unlock()
				m.watch(session)
				m.logger.Info("AMQP reconnected", log.Context{"attempt": attempt})
				return
			}
		}

		if !time.Now().Before(deadline) {
			m.logger.Error("AMQP reconnection budget exhausted; giving up", log.Context{
				"attempts": attempt,
				"window":   m.cfg.MaxReconnectWindow.String(),
				"error":    err.Error(),
			})
			if m.onGiveUp != nil {
				m.onGiveUp()
			}
			return
		}

		backoff := m.backoffFor(attempt)
		m.logger.Warn("AMQP reconnect attempt failed; retrying", log.Context{
			"attempt": attempt,
			"backoff": backoff.String(),
			"error":   err.Error(),
		})

		select {
		case <-m.closed:
			return
		case <-time.After(backoff):
		}
	}
}

func (m *ConnectionManager) applyAllTopologies(session Session) error {
	m.mu.RLock()
	topos := make([]func(*amqp.Channel) error, len(m.topologies))
	copy(topos, m.topologies)
	m.mu.RUnlock()

	for _, setup := range topos {
		if err := applyTopology(session, setup); err != nil {
			return err
		}
	}
	return nil
}

func (m *ConnectionManager) backoffFor(attempt int) time.Duration {
	base := float64(m.cfg.MinBackoff) * math.Pow(2, float64(attempt-1))
	if base > float64(m.cfg.MaxBackoff) {
		base = float64(m.cfg.MaxBackoff)
	}
	// Full jitter across [0, MinBackoff) to avoid thundering-herd reconnects.
	jitter := time.Duration(rand.Int63n(int64(m.cfg.MinBackoff)))
	return time.Duration(base) + jitter
}

// applyTopology opens a channel on the session, runs the declaration, and
// closes the channel.
func applyTopology(session Session, setup func(*amqp.Channel) error) error {
	if session == nil {
		return ErrNotConnected
	}
	ch, err := session.Channel()
	if err != nil {
		return fmt.Errorf("amqp: open channel for topology: %w", err)
	}
	if ch != nil {
		defer ch.Close()
	}
	return setup(ch)
}
