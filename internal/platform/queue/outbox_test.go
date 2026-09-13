package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"

	"github.com/google/uuid"
)

// These tests exercise outbox-consumer behavior in scenarios that map to a
// multi-replica (HA) worker deployment. They require a running Postgres with
// the queue_outbox table already migrated. Set SQL_HOST/PORT/DATABASE/USER/
// PASSWORD env vars; tests t.Skip cleanly when SQL_HOST is unset.
//
// Mapping of guarantees verified here:
//   - SKIP LOCKED prevents duplicate dispatch across concurrent consumers
//   - All rows dispatched exactly once even under high consumer concurrency
//   - Locked rows are skipped (not blocking) by peer consumers
//   - Dispatch failure increments retry_count exactly once even under contention
//   - Rows past MaxRetries are skipped without blocking the queue
//   - Rows with future available_at are not picked up early
//   - Rolled-back txs release the row for the next consumer (crash recovery)
//   - Malformed payloads are dropped, not stuck
//   - Idle table returns sql.ErrNoRows cleanly

func setupOutboxDB(t *testing.T) *sql.DB {
	t.Helper()

	host := os.Getenv("SQL_HOST")
	if host == "" {
		t.Skip("SQL_HOST not set; skipping outbox HA tests")
	}

	maxPool := 50
	if v, err := strconv.Atoi(os.Getenv("SQL_MAX_POOL_CONN")); err == nil && v > maxPool {
		maxPool = v
	}

	db := sql.NewDB(sql.Conf{
		Host:        host,
		Port:        os.Getenv("SQL_PORT"),
		Name:        os.Getenv("SQL_DATABASE"),
		User:        os.Getenv("SQL_USER"),
		Password:    os.Getenv("SQL_PASSWORD"),
		MaxPoolConn: maxPool,
	})

	if err := db.Ping(); err != nil {
		t.Skipf("cannot reach Postgres at %s: %v", host, err)
	}

	var exists bool
	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables WHERE table_name = 'queue_outbox'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("schema check: %v", err)
	}
	if !exists {
		t.Skip("queue_outbox table missing — run migrations against the test DB first")
	}

	truncateOutbox(t, db)
	t.Cleanup(func() {
		truncateOutbox(t, db)
		_ = db.Close()
	})
	return db
}

func truncateOutbox(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("TRUNCATE TABLE queue_outbox"); err != nil {
		t.Fatalf("truncate queue_outbox: %v", err)
	}
}

func testLogger() *log.Logger {
	return log.NewLogger("outbox-test", log.NewHandler(os.Stdout, "dev"))
}

type mockDispatcher struct {
	mu         sync.Mutex
	dispatched map[string]int
	err        error
	delay      time.Duration
}

func newMockDispatcher() *mockDispatcher {
	return &mockDispatcher{dispatched: make(map[string]int)}
}

func (m *mockDispatcher) Dispatch(ctx context.Context, queueName string, msg *Message) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.dispatched[msg.ID]++
	return nil
}

func (m *mockDispatcher) totalDispatches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, c := range m.dispatched {
		total += c
	}
	return total
}

func (m *mockDispatcher) uniqueDispatches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.dispatched)
}

func (m *mockDispatcher) countFor(msgID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dispatched[msgID]
}

type insertOpt func(*insertRow)

type insertRow struct {
	id          string
	queueName   string
	message     *Message
	availableAt time.Time
	retryCount  int
	rawJSON     []byte
}

func withAvailableAt(at time.Time) insertOpt { return func(r *insertRow) { r.availableAt = at } }
func withRetryCount(n int) insertOpt         { return func(r *insertRow) { r.retryCount = n } }
func withRawJSON(b []byte) insertOpt         { return func(r *insertRow) { r.rawJSON = b } }

func insertOutboxRow(t *testing.T, db *sql.DB, queueName string, msg *Message, opts ...insertOpt) string {
	t.Helper()
	row := insertRow{
		id:          uuid.NewString(),
		queueName:   queueName,
		message:     msg,
		availableAt: time.Now(),
		retryCount:  0,
	}
	for _, o := range opts {
		o(&row)
	}

	var payload []byte
	if row.rawJSON != nil {
		payload = row.rawJSON
	} else {
		j, err := json.Marshal(row.message)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = j
	}

	if _, err := db.Exec(`
		INSERT INTO queue_outbox (id, queue_name, message, created_at, available_at, retry_count)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, row.id, row.queueName, payload, time.Now(), row.availableAt, row.retryCount); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return row.id
}

func countOutboxRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM queue_outbox`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func readOutboxRow(t *testing.T, db *sql.DB, id string) (retryCount int, availableAt time.Time, lastError sql.NullString) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT retry_count, available_at, last_error FROM queue_outbox WHERE id = $1`, id,
	).Scan(&retryCount, &availableAt, &lastError); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return
}

// dumpOutboxState logs every row currently in queue_outbox. Used to diagnose
// why a drain assertion failed (e.g. row stuck because retry_count is bumped,
// available_at is in the future, or some lock is held longer than expected).
func dumpOutboxState(t *testing.T, db *sql.DB, label string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT id, queue_name, available_at, retry_count, last_error
		FROM queue_outbox
		ORDER BY available_at ASC
	`)
	if err != nil {
		t.Logf("[dump:%s] query failed: %v", label, err)
		return
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, qn string
		var availAt time.Time
		var retry int
		var lastErr sql.NullString
		if err := rows.Scan(&id, &qn, &availAt, &retry, &lastErr); err != nil {
			t.Logf("[dump:%s] scan err: %v", label, err)
			continue
		}
		n++
		t.Logf("[dump:%s] row id=%s queue=%s available_at=%s (Δ=%v) retry=%d last_error=%q",
			label, id, qn, availAt.UTC().Format(time.RFC3339Nano),
			time.Until(availAt).Round(time.Millisecond), retry, lastErr.String)
	}
	t.Logf("[dump:%s] total rows=%d (now=%s)", label, n, time.Now().UTC().Format(time.RFC3339Nano))
}

// drainOutbox runs N consumer goroutines that loop calling
// processNextOutboxMessage until the context is cancelled. Unlike a
// quit-on-ErrNoRows pattern, consumers keep retrying so transient ErrNoRows
// (from peers holding locks) doesn't cause premature exit. Returns a function
// to stop them and wait.
func drainOutbox(
	t *testing.T,
	db *sql.DB,
	dispatcher outboxDispatcher,
	logger *log.Logger,
	consumers int,
) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				err := processNextOutboxMessage(ctx, db, dispatcher, logger)
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, sql.ErrNoRows) {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				if err != nil {
					t.Errorf("process error: %v", err)
					return
				}
			}
		}()
	}
	return func() {
		cancel()
		wg.Wait()
	}
}

// waitForOutboxEmpty polls every 20ms until queue_outbox has zero rows, or the
// deadline expires. Returns the remaining row count (0 on success).
func waitForOutboxEmpty(t *testing.T, db *sql.DB, deadline time.Duration) int {
	t.Helper()
	timeout := time.After(deadline)
	for {
		if n := countOutboxRows(t, db); n == 0 {
			return 0
		}
		select {
		case <-timeout:
			return countOutboxRows(t, db)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// HA-1: With a single outbox row and N concurrent consumers, expect exactly
// one dispatch. This is the headline test for FOR UPDATE SKIP LOCKED — without
// it the previous code would dispatch the same row N times.
func TestOutbox_ConcurrentConsumers_NoDuplicateDispatch(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, err := NewMessage("hello", map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("new msg: %v", err)
	}
	insertOutboxRow(t, db, "test-queue", msg)

	dispatcher := newMockDispatcher()
	// A small delay forces real contention: the winning consumer holds the row
	// lock long enough that peers definitely see a locked row.
	dispatcher.delay = 50 * time.Millisecond

	const consumers = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = processNextOutboxMessage(t.Context(), db, dispatcher, logger)
		}()
	}
	close(start)
	wg.Wait()

	if got := dispatcher.totalDispatches(); got != 1 {
		t.Fatalf("expected exactly 1 dispatch under %d concurrent consumers, got %d", consumers, got)
	}
	if n := countOutboxRows(t, db); n != 0 {
		t.Fatalf("expected outbox empty after success, got %d rows", n)
	}
}

// HA-2: N rows, M concurrent consumers running to drain — every row dispatched
// exactly once, no losses, no duplicates, table empty afterwards. Uses the
// "drain pattern": consumers keep polling on ErrNoRows so transient empty
// reads (from peers holding locks) don't terminate consumers prematurely.
func TestOutbox_ManyRowsManyConsumers_ExactlyOnceDispatch(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	const rowCount = 50
	msgIDs := make([]string, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		msg, _ := NewMessage("hello", map[string]int{"i": i})
		msgIDs = append(msgIDs, msg.ID)
		insertOutboxRow(t, db, "test-queue", msg)
	}

	dispatcher := newMockDispatcher()

	stop := drainOutbox(t, db, dispatcher, logger, 10)
	if remaining := waitForOutboxEmpty(t, db, 10*time.Second); remaining != 0 {
		dumpOutboxState(t, db, "ha2-drain-timeout")
		stop()
		t.Fatalf("outbox did not drain: dispatched=%d (unique=%d), remaining=%d",
			dispatcher.totalDispatches(), dispatcher.uniqueDispatches(), remaining)
	}
	stop()

	if got := dispatcher.totalDispatches(); got != rowCount {
		t.Fatalf("expected %d total dispatches, got %d", rowCount, got)
	}
	if got := dispatcher.uniqueDispatches(); got != rowCount {
		t.Fatalf("expected %d unique dispatches (no duplicates), got %d", rowCount, got)
	}
	for _, id := range msgIDs {
		if c := dispatcher.countFor(id); c != 1 {
			t.Errorf("message %s dispatched %d times, want exactly 1", id, c)
		}
	}
}

// HA-3: A row locked by one tx is *skipped* by a concurrent consumer — it does
// not block. Without SKIP LOCKED the second consumer would wait until the
// first commits/rolls back.
func TestOutbox_LockedRow_IsSkippedNotBlocking(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, _ := NewMessage("hello", map[string]string{"k": "v"})
	insertOutboxRow(t, db, "test-queue", msg)

	txA, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin tx A: %v", err)
	}
	defer func() { _ = txA.Rollback() }()

	lockedMsg, _, err := lockNextOutboxMessage(t.Context(), txA)
	if err != nil {
		t.Fatalf("lockNextOutboxMessage: %v", err)
	}
	if lockedMsg == nil {
		t.Fatal("expected tx A to lock the row")
	}

	done := make(chan error, 1)
	go func() {
		dispatcher := newMockDispatcher()
		done <- processNextOutboxMessage(t.Context(), db, dispatcher, logger)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected peer consumer to see sql.ErrNoRows, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer consumer is blocking on a locked row — FOR UPDATE SKIP LOCKED not working")
	}
}

// HA-4: Dispatch failure schedules retry (retry_count incremented, available_at
// in the future, last_error set) and the row remains in the table.
func TestOutbox_DispatchFailure_SchedulesRetryNoLoss(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, _ := NewMessage("hello", map[string]string{"k": "v"})
	id := insertOutboxRow(t, db, "test-queue", msg)

	dispatcher := newMockDispatcher()
	dispatcher.err = errors.New("amqp unavailable")

	if err := processNextOutboxMessage(t.Context(), db, dispatcher, logger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := dispatcher.totalDispatches(); got != 0 {
		t.Fatalf("expected 0 successful dispatches, got %d", got)
	}

	rc, availAt, lastErr := readOutboxRow(t, db, id)
	if rc != 1 {
		t.Fatalf("expected retry_count=1, got %d", rc)
	}
	if !availAt.After(time.Now()) {
		t.Fatalf("expected available_at in the future, got %v", availAt)
	}
	if !lastErr.Valid || lastErr.String != "amqp unavailable" {
		t.Fatalf("expected last_error='amqp unavailable', got %+v", lastErr)
	}
}

// HA-5: Concurrent dispatch failures against the same row increment retry_count
// exactly once — the retry UPDATE happens under the same row lock as the SELECT.
// Without locking, N concurrent failures would bump retry_count N times and
// prematurely trip MaxRetries.
func TestOutbox_ConcurrentDispatchFailures_RetryIncrementedOnce(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, _ := NewMessage("hello", map[string]string{"k": "v"})
	id := insertOutboxRow(t, db, "test-queue", msg)

	dispatcher := newMockDispatcher()
	dispatcher.err = errors.New("boom")
	dispatcher.delay = 30 * time.Millisecond

	const consumers = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = processNextOutboxMessage(t.Context(), db, dispatcher, logger)
		}()
	}
	close(start)
	wg.Wait()

	rc, _, _ := readOutboxRow(t, db, id)
	if rc != 1 {
		t.Fatalf("expected retry_count=1 even under %d concurrent failures, got %d", consumers, rc)
	}
}

// HA-6: Rows that reached MaxRetries are skipped (left as dead-letter); their
// presence does not block other ready rows from being picked up.
func TestOutbox_RetryCountCapped_DoesNotBlockOtherRows(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	deadMsg, _ := NewMessage("hello", map[string]string{"dead": "yes"})
	deadID := insertOutboxRow(t, db, "test-queue", deadMsg, withRetryCount(MaxRetries))

	aliveMsg, _ := NewMessage("hello", map[string]string{"alive": "yes"})
	insertOutboxRow(t, db, "test-queue", aliveMsg)

	dispatcher := newMockDispatcher()
	if err := processNextOutboxMessage(t.Context(), db, dispatcher, logger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c := dispatcher.countFor(aliveMsg.ID); c != 1 {
		t.Fatalf("expected alive message dispatched once, got %d", c)
	}
	if c := dispatcher.countFor(deadMsg.ID); c != 0 {
		t.Fatalf("expected dead message NOT dispatched, got %d", c)
	}

	if n := countOutboxRows(t, db); n != 1 {
		t.Fatalf("expected 1 dead row remaining, got %d", n)
	}
	rc, _, _ := readOutboxRow(t, db, deadID)
	if rc != MaxRetries {
		t.Fatalf("dead row retry_count changed unexpectedly: got %d", rc)
	}
}

// HA-7: Rows scheduled in the future (available_at > now) are not picked up
// early. Critical for the exponential-backoff retry path.
func TestOutbox_FutureAvailableAt_NotPickedEarly(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, _ := NewMessage("hello", map[string]string{"k": "v"})
	insertOutboxRow(t, db, "test-queue", msg, withAvailableAt(time.Now().Add(1*time.Hour)))

	dispatcher := newMockDispatcher()
	err := processNextOutboxMessage(t.Context(), db, dispatcher, logger)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
	if got := dispatcher.totalDispatches(); got != 0 {
		t.Fatalf("expected 0 dispatches, got %d", got)
	}
	if n := countOutboxRows(t, db); n != 1 {
		t.Fatalf("expected row to remain in table, got %d", n)
	}
}

// HA-8: A rolled-back tx (worker crashed / network blip) releases the row so
// the next consumer can claim and dispatch it. This is the recovery story for
// crash-mid-publish: at-least-once delivery, never lost.
func TestOutbox_RolledBackTx_RowRecoveredByNextConsumer(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	msg, _ := NewMessage("hello", map[string]string{"k": "v"})
	insertOutboxRow(t, db, "test-queue", msg)

	txA, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, _, err := lockNextOutboxMessage(t.Context(), txA); err != nil {
		t.Fatalf("lock failed: %v", err)
	}
	if err := txA.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	dispatcher := newMockDispatcher()
	if err := processNextOutboxMessage(t.Context(), db, dispatcher, logger); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := dispatcher.countFor(msg.ID); got != 1 {
		t.Fatalf("expected dispatch after rollback recovery, got count=%d", got)
	}
	if n := countOutboxRows(t, db); n != 0 {
		t.Fatalf("expected outbox empty, got %d rows", n)
	}
}

// HA-9: A row whose JSON decodes but is structurally invalid for *Message
// (e.g. wrong type for a field) is dropped, not stuck blocking the consumer.
func TestOutbox_MalformedMessage_DroppedNotStuck(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	// Valid JSON, but `id` is a number — *Message.ID is a string, so
	// json.Unmarshal returns a type error.
	malformed := []byte(`{"id": 12345, "name": "broken"}`)
	insertOutboxRow(t, db, "test-queue", nil, withRawJSON(malformed))

	// Insert a valid row after it so we can confirm the consumer recovers.
	goodMsg, _ := NewMessage("hello", map[string]string{"k": "v"})
	insertOutboxRow(t, db, "test-queue", goodMsg)

	dispatcher := newMockDispatcher()

	// First call drops the malformed row.
	if err := processNextOutboxMessage(t.Context(), db, dispatcher, logger); err != nil {
		t.Fatalf("first process: %v", err)
	}
	if got := dispatcher.totalDispatches(); got != 0 {
		t.Fatalf("expected malformed row to NOT be dispatched, got %d", got)
	}

	// Second call dispatches the good row.
	if err := processNextOutboxMessage(t.Context(), db, dispatcher, logger); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if got := dispatcher.countFor(goodMsg.ID); got != 1 {
		t.Fatalf("expected good row dispatched once, got %d", got)
	}

	if n := countOutboxRows(t, db); n != 0 {
		t.Fatalf("expected outbox empty (both rows gone), got %d", n)
	}
}

// HA-10: An idle (empty) outbox returns sql.ErrNoRows cleanly — no panic, no
// side-effects on the dispatcher.
func TestOutbox_Idle_ReturnsNoRows(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	dispatcher := newMockDispatcher()
	err := processNextOutboxMessage(t.Context(), db, dispatcher, logger)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows on empty outbox, got %v", err)
	}
	if got := dispatcher.totalDispatches(); got != 0 {
		t.Fatalf("expected 0 dispatches, got %d", got)
	}
}

// HA-11: drains a fully populated outbox via the public-style drain pattern.
// Exercises N consumers cooperating on a populated queue end-to-end.
func TestOutbox_ConsumeOutboxMessages_DrainsTableEndToEnd(t *testing.T) {
	db := setupOutboxDB(t)
	logger := testLogger()

	const rowCount = 30
	for i := 0; i < rowCount; i++ {
		msg, _ := NewMessage("hello", map[string]int{"i": i})
		insertOutboxRow(t, db, "test-queue", msg)
	}

	dispatcher := newMockDispatcher()

	stop := drainOutbox(t, db, dispatcher, logger, 5)
	if remaining := waitForOutboxEmpty(t, db, 15*time.Second); remaining != 0 {
		dumpOutboxState(t, db, "ha11-drain-timeout")
		stop()
		t.Fatalf("outbox did not drain: dispatched=%d (unique=%d), remaining=%d",
			dispatcher.totalDispatches(), dispatcher.uniqueDispatches(), remaining)
	}
	stop()

	if got := dispatcher.totalDispatches(); got != rowCount {
		t.Fatalf("expected %d total dispatches, got %d", rowCount, got)
	}
	if got := dispatcher.uniqueDispatches(); got != rowCount {
		t.Fatalf("expected %d unique dispatches, got %d (duplicates exist)", rowCount, got)
	}
}

// Sanity: *Pool satisfies outboxDispatcher (compile-time check + readable error
// at test build time if the interface ever drifts from the production type).
var _ outboxDispatcher = (*Pool)(nil)
