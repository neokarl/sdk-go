package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// The transactional outbox closes the publish/commit gap: a producer inserts an
// outbox row in the SAME database transaction as its state change, so the event
// is durably recorded iff the state change commits. A relay then ships
// unpublished rows to the bus (at-least-once) and marks them sent. Neither a
// crash after commit nor a failed publish can lose the event.

// OutboxDDL creates the outbox table. Include it in the producer's migrations.
const OutboxDDL = `
CREATE TABLE IF NOT EXISTS outbox (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    source       TEXT NOT NULL,
    tenant_id    TEXT,
    payload      JSONB,
    created_at   TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;`

// Execer is the subset of *sql.Tx / *sql.DB the outbox write needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func outboxInsert(e *Event) (string, []any) {
	e.ensureMeta()
	payload, _ := json.Marshal(e.Payload)
	return `INSERT INTO outbox (id, name, source, tenant_id, payload, created_at)
	        VALUES ($1, $2, $3, $4, $5, $6)`,
		[]any{e.ID, e.Name, e.Source, e.TenantID, string(payload), e.Timestamp}
}

// OutboxInsert returns the INSERT statement + args for an outbox row, for
// callers that run it through their own ORM/transaction (e.g. gorm's
// tx.Exec(query, args...)). Fills id + timestamp if blank.
func OutboxInsert(e Event) (query string, args []any) { return outboxInsert(&e) }

// WriteOutbox inserts an outbox row via a database/sql executor — pass the same
// *sql.Tx used for the state change so the two commit atomically.
func WriteOutbox(ctx context.Context, tx Execer, e Event) error {
	q, args := outboxInsert(&e)
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// OutboxRelay polls the outbox for unpublished events and ships them to the bus.
type OutboxRelay struct {
	db       *sql.DB
	pub      Publisher
	log      *slog.Logger
	interval time.Duration
	batch    int
}

// NewOutboxRelay builds a relay over a database/sql handle (a gorm user gets one
// via gormDB.DB()) and a publisher.
func NewOutboxRelay(db *sql.DB, pub Publisher, log *slog.Logger) *OutboxRelay {
	if log == nil {
		log = slog.Default()
	}
	return &OutboxRelay{db: db, pub: pub, log: log, interval: time.Second, batch: 100}
}

// Run drains the outbox on an interval until ctx is cancelled.
func (r *OutboxRelay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := r.drain(ctx); n > 0 {
				r.log.LogAttrs(ctx, slog.LevelDebug, "outbox.relayed", slog.Int("count", n))
			}
		}
	}
}

func (r *OutboxRelay) drain(ctx context.Context) int {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, source, tenant_id, payload, created_at
		   FROM outbox WHERE published_at IS NULL ORDER BY created_at LIMIT $1`, r.batch)
	if err != nil {
		r.log.Warn("outbox.query", "err", err)
		return 0
	}
	var pending []Event
	for rows.Next() {
		var (
			e       Event
			tenant  sql.NullString
			payload []byte
		)
		if err := rows.Scan(&e.ID, &e.Name, &e.Source, &tenant, &payload, &e.Timestamp); err != nil {
			r.log.Warn("outbox.scan", "err", err)
			continue
		}
		e.TenantID = tenant.String
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &e.Payload)
		}
		pending = append(pending, e)
	}
	rows.Close()

	published := 0
	for _, e := range pending {
		if err := r.pub.Publish(ctx, e); err != nil {
			// Stop on first failure — preserve order; retry next tick.
			r.log.Warn("outbox.publish", "id", e.ID, "err", err)
			break
		}
		if _, err := r.db.ExecContext(ctx,
			`UPDATE outbox SET published_at = now() WHERE id = $1`, e.ID); err != nil {
			// Published but not marked → will republish; consumers dedup.
			r.log.Warn("outbox.mark", "id", e.ID, "err", err)
			break
		}
		published++
	}
	return published
}
