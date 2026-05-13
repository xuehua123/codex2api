package database

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	requestTraceBatchSize     = 200
	requestTraceFlushInterval = time.Second
	maxRequestTraceBuffer     = 20000
)

type requestTraceEntry struct {
	RequestID      string
	APIKeyID       int64
	APIKeyName     string
	APIKeyMasked   string
	AccountID      int64
	Endpoint       string
	Model          string
	EffectiveModel string
	Stream         bool
	Stage          string
	Attempt        int
	ElapsedMs      int
	StatusCode     int
	ErrorKind      string
	Message        string
}

type RequestTraceEventInput struct {
	RequestID      string
	APIKeyID       int64
	APIKeyName     string
	APIKeyMasked   string
	AccountID      int64
	Endpoint       string
	Model          string
	EffectiveModel string
	Stream         bool
	Stage          string
	Attempt        int
	ElapsedMs      int
	StatusCode     int
	ErrorKind      string
	Message        string
}

type RequestTraceEvent struct {
	ID             int64     `json:"id"`
	RequestID      string    `json:"request_id"`
	APIKeyID       int64     `json:"api_key_id"`
	APIKeyName     string    `json:"api_key_name"`
	APIKeyMasked   string    `json:"api_key_masked"`
	AccountID      int64     `json:"account_id"`
	Endpoint       string    `json:"endpoint"`
	Model          string    `json:"model"`
	EffectiveModel string    `json:"effective_model"`
	Stream         bool      `json:"stream"`
	Stage          string    `json:"stage"`
	Attempt        int       `json:"attempt"`
	ElapsedMs      int       `json:"elapsed_ms"`
	StatusCode     int       `json:"status_code"`
	ErrorKind      string    `json:"error_kind"`
	Message        string    `json:"message"`
	CreatedAt      time.Time `json:"created_at"`
}

type RequestTraceEventFilter struct {
	Start     time.Time
	End       time.Time
	Limit     int
	RequestID string
	APIKeyID  *int64
	AccountID *int64
	Stage     string
	ErrorOnly bool
}

func (db *DB) InsertRequestTraceEvent(ctx context.Context, input *RequestTraceEventInput) error {
	if db == nil || input == nil {
		return nil
	}
	entry := requestTraceEntry{
		RequestID:      strings.TrimSpace(input.RequestID),
		APIKeyID:       input.APIKeyID,
		APIKeyName:     strings.TrimSpace(input.APIKeyName),
		APIKeyMasked:   strings.TrimSpace(input.APIKeyMasked),
		AccountID:      input.AccountID,
		Endpoint:       strings.TrimSpace(input.Endpoint),
		Model:          strings.TrimSpace(input.Model),
		EffectiveModel: strings.TrimSpace(input.EffectiveModel),
		Stream:         input.Stream,
		Stage:          strings.TrimSpace(input.Stage),
		Attempt:        input.Attempt,
		ElapsedMs:      input.ElapsedMs,
		StatusCode:     input.StatusCode,
		ErrorKind:      strings.TrimSpace(input.ErrorKind),
		Message:        strings.TrimSpace(input.Message),
	}
	if entry.RequestID == "" || entry.Stage == "" {
		return nil
	}

	db.traceMu.Lock()
	if len(db.traceBuf) >= maxRequestTraceBuffer {
		db.traceMu.Unlock()
		return db.insertRequestTraceEventsSync(ctx, []requestTraceEntry{entry})
	}
	db.traceBuf = append(db.traceBuf, entry)
	bufLen := len(db.traceBuf)
	db.traceMu.Unlock()

	if bufLen >= requestTraceBatchSize || shouldFlushRequestTraceStage(entry.Stage) {
		db.notifyTraceFlush()
	}
	return nil
}

func shouldFlushRequestTraceStage(stage string) bool {
	switch stage {
	case "request_start", "upstream_start", "upstream_headers", "upstream_error", "request_completed", "request_failed":
		return true
	default:
		return false
	}
}

func (db *DB) startTraceFlusher() {
	db.traceWg.Add(1)
	go func() {
		defer db.traceWg.Done()
		for {
			timer := time.NewTimer(requestTraceFlushInterval)
			select {
			case <-timer.C:
				db.flushRequestTraceEvents()
			case <-db.traceFlushNotify:
				stopTimer(timer)
				db.flushRequestTraceEvents()
			case <-db.traceStop:
				stopTimer(timer)
				return
			}
		}
	}()
}

func (db *DB) notifyTraceFlush() {
	if db == nil || db.traceFlushNotify == nil {
		return
	}
	select {
	case db.traceFlushNotify <- struct{}{}:
	default:
	}
}

func (db *DB) flushRequestTraceEvents() {
	db.traceMu.Lock()
	if len(db.traceBuf) == 0 {
		db.traceMu.Unlock()
		return
	}
	batch := db.traceBuf
	db.traceBuf = make([]requestTraceEntry, 0, requestTraceBatchSize)
	db.traceMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.insertRequestTraceEventsSync(ctx, batch); err != nil {
		log.Printf("写入请求诊断事件失败: %v", err)
		db.requeueRequestTraceEvents(batch)
	}
}

func (db *DB) requeueRequestTraceEvents(batch []requestTraceEntry) {
	if db == nil || len(batch) == 0 {
		return
	}
	db.traceMu.Lock()
	defer db.traceMu.Unlock()
	available := maxRequestTraceBuffer - len(db.traceBuf)
	if available <= 0 {
		log.Printf("请求诊断事件缓冲已满，丢弃 %d 条事件", len(batch))
		return
	}
	if len(batch) > available {
		log.Printf("请求诊断事件缓冲接近上限，保留 %d/%d 条事件", available, len(batch))
		batch = batch[len(batch)-available:]
	}
	requeued := make([]requestTraceEntry, 0, len(batch)+len(db.traceBuf))
	requeued = append(requeued, batch...)
	requeued = append(requeued, db.traceBuf...)
	db.traceBuf = requeued
}

func (db *DB) insertRequestTraceEventsSync(ctx context.Context, batch []requestTraceEntry) error {
	if db == nil || len(batch) == 0 {
		return nil
	}
	if db.driver == "postgres" {
		return db.batchInsertRequestTraceEvents(ctx, batch)
	}
	return db.insertRequestTraceEventsSQLite(ctx, batch)
}

func (db *DB) batchInsertRequestTraceEvents(ctx context.Context, batch []requestTraceEntry) error {
	const valuesPerRow = 15
	const maxRowsPerBatch = 4000
	for start := 0; start < len(batch); start += maxRowsPerBatch {
		end := start + maxRowsPerBatch
		if end > len(batch) {
			end = len(batch)
		}
		if err := db.batchInsertRequestTraceEventsChunk(ctx, batch[start:end], valuesPerRow); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) batchInsertRequestTraceEventsChunk(ctx context.Context, batch []requestTraceEntry, valuesPerRow int) error {
	valueStrings := make([]string, 0, len(batch))
	valueArgs := make([]interface{}, 0, len(batch)*valuesPerRow)
	argIdx := 1
	for _, e := range batch {
		valueStrings = append(valueStrings, fmt.Sprintf(
			"($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			argIdx, argIdx+1, argIdx+2, argIdx+3, argIdx+4, argIdx+5, argIdx+6, argIdx+7, argIdx+8, argIdx+9, argIdx+10, argIdx+11, argIdx+12, argIdx+13, argIdx+14,
		))
		valueArgs = append(valueArgs,
			e.RequestID, e.APIKeyID, e.APIKeyName, e.APIKeyMasked, e.AccountID,
			e.Endpoint, e.Model, e.EffectiveModel, e.Stream, e.Stage,
			e.Attempt, e.ElapsedMs, e.StatusCode, e.ErrorKind, e.Message,
		)
		argIdx += valuesPerRow
	}
	query := fmt.Sprintf(`INSERT INTO request_trace_events (
		request_id, api_key_id, api_key_name, api_key_masked, account_id,
		endpoint, model, effective_model, stream, stage,
		attempt, elapsed_ms, status_code, error_kind, message
	) VALUES %s`, strings.Join(valueStrings, ","))
	_, err := db.conn.ExecContext(ctx, query, valueArgs...)
	return err
}

func (db *DB) insertRequestTraceEventsSQLite(ctx context.Context, batch []requestTraceEntry) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO request_trace_events (
		request_id, api_key_id, api_key_name, api_key_masked, account_id,
		endpoint, model, effective_model, stream, stage,
		attempt, elapsed_ms, status_code, error_kind, message
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, e := range batch {
		if _, err := stmt.ExecContext(ctx,
			e.RequestID, e.APIKeyID, e.APIKeyName, e.APIKeyMasked, e.AccountID,
			e.Endpoint, e.Model, e.EffectiveModel, e.Stream, e.Stage,
			e.Attempt, e.ElapsedMs, e.StatusCode, e.ErrorKind, e.Message,
		); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ListRequestTraceEvents(ctx context.Context, f RequestTraceEventFilter) ([]*RequestTraceEvent, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	if f.End.IsZero() {
		f.End = time.Now()
	}
	if f.Start.IsZero() {
		f.Start = f.End.Add(-30 * time.Minute)
	}

	startArg, endArg := db.timeRangeArgs(f.Start, f.End)
	parts := []string{`created_at >= $1 AND created_at <= $2`}
	args := []interface{}{startArg, endArg}
	paramIdx := 3
	addArg := func(value interface{}) string {
		placeholder := fmt.Sprintf("$%d", paramIdx)
		args = append(args, value)
		paramIdx++
		return placeholder
	}

	if f.RequestID != "" {
		p := addArg(f.RequestID)
		parts = append(parts, fmt.Sprintf(`request_id = %s`, p))
	}
	if f.APIKeyID != nil {
		p := addArg(*f.APIKeyID)
		parts = append(parts, fmt.Sprintf(`COALESCE(api_key_id, 0) = %s`, p))
	}
	if f.AccountID != nil {
		p := addArg(*f.AccountID)
		parts = append(parts, fmt.Sprintf(`COALESCE(account_id, 0) = %s`, p))
	}
	if f.Stage != "" {
		p := addArg(f.Stage)
		parts = append(parts, fmt.Sprintf(`stage = %s`, p))
	}
	if f.ErrorOnly {
		parts = append(parts, `(COALESCE(status_code, 0) >= 400 OR COALESCE(error_kind, '') <> '' OR stage IN ('request_failed', 'upstream_error', 'response_failed'))`)
	}
	limitParam := addArg(f.Limit)

	rows, err := db.conn.QueryContext(ctx, `SELECT
		id, request_id, COALESCE(api_key_id, 0), COALESCE(api_key_name, ''), COALESCE(api_key_masked, ''),
		COALESCE(account_id, 0), COALESCE(endpoint, ''), COALESCE(model, ''), COALESCE(effective_model, ''),
		COALESCE(stream, false), stage, COALESCE(attempt, 0), COALESCE(elapsed_ms, 0),
		COALESCE(status_code, 0), COALESCE(error_kind, ''), COALESCE(message, ''), created_at
		FROM request_trace_events
		WHERE `+strings.Join(parts, " AND ")+`
		ORDER BY created_at DESC, id DESC
		LIMIT `+limitParam, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]*RequestTraceEvent, 0, f.Limit)
	for rows.Next() {
		event := &RequestTraceEvent{}
		var createdAtRaw interface{}
		if err := rows.Scan(
			&event.ID, &event.RequestID, &event.APIKeyID, &event.APIKeyName, &event.APIKeyMasked,
			&event.AccountID, &event.Endpoint, &event.Model, &event.EffectiveModel,
			&event.Stream, &event.Stage, &event.Attempt, &event.ElapsedMs,
			&event.StatusCode, &event.ErrorKind, &event.Message, &createdAtRaw,
		); err != nil {
			return nil, err
		}
		createdAt, err := parseDBTimeValue(createdAtRaw)
		if err != nil {
			return nil, err
		}
		event.CreatedAt = createdAt
		events = append(events, event)
	}
	if events == nil {
		events = []*RequestTraceEvent{}
	}
	return events, rows.Err()
}

func (db *DB) ClearOldRequestTraceEvents(ctx context.Context, olderThan time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	result, err := db.conn.ExecContext(ctx, `DELETE FROM request_trace_events WHERE created_at < $1`, db.timeArg(olderThan))
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}
