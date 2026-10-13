package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Timed connections log statements and transactions that take longer than a
// threshold. A query is timed until its rows are closed, so the time includes
// reading the results. A transaction is timed from begin to commit or rollback,
// which is how long it holds the database's writer. Bound arguments are never
// logged, only the statement text.
const timedSQLiteDriverName = "sqlite-timed"

var slowQueryThreshold atomic.Int64

func init() {
	base, err := sql.Open("sqlite", "")
	if err != nil {
		panic(err)
	}
	sql.Register(timedSQLiteDriverName, timedDriver{base: base.Driver()})
	_ = base.Close()
}

// SetSlowQueryThreshold enables slow statement logging for databases opened
// afterwards. Zero or a negative value disables it.
func SetSlowQueryThreshold(threshold time.Duration) {
	slowQueryThreshold.Store(int64(threshold))
}

func sqliteDriverName() string {
	if slowQueryThreshold.Load() > 0 {
		return timedSQLiteDriverName
	}
	return "sqlite"
}

func logIfSlow(kind, query string, started time.Time) {
	threshold := time.Duration(slowQueryThreshold.Load())
	elapsed := time.Since(started)
	if threshold <= 0 || elapsed < threshold {
		return
	}
	log.Printf("storage: slow %s %dms: %s", kind, elapsed.Milliseconds(), compactQuery(query))
}

func compactQuery(query string) string {
	query = strings.Join(strings.Fields(query), " ")
	const limit = 400
	if len(query) > limit {
		return query[:limit] + "…"
	}
	return query
}

type timedDriver struct{ base driver.Driver }

func (d timedDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &timedConn{base: conn}, nil
}

type timedConn struct {
	base       driver.Conn
	txStarted  time.Time
	txFirstSQL string
}

func (c *timedConn) noteStatement(query string) {
	if !c.txStarted.IsZero() && c.txFirstSQL == "" {
		c.txFirstSQL = query
	}
}

func (c *timedConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *timedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	preparer, ok := c.base.(driver.ConnPrepareContext)
	var stmt driver.Stmt
	var err error
	if ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		stmt, err = c.base.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &timedStmt{base: stmt, conn: c, query: query}, nil
}

func (c *timedConn) Close() error { return c.base.Close() }

func (c *timedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *timedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.base.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("sqlite connection does not support BeginTx")
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	c.txStarted, c.txFirstSQL = time.Now(), ""
	return &timedTx{base: tx, conn: c}, nil
}

func (c *timedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := c.base.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.noteStatement(query)
	started := time.Now()
	result, err := execer.ExecContext(ctx, query, args)
	logIfSlow("statement", query, started)
	return result, err
}

func (c *timedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := c.base.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.noteStatement(query)
	started := time.Now()
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		logIfSlow("query", query, started)
		return nil, err
	}
	return &timedRows{base: rows, query: query, started: started}, nil
}

func (c *timedConn) Ping(ctx context.Context) error {
	if pinger, ok := c.base.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *timedConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.base.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *timedConn) IsValid() bool {
	if validator, ok := c.base.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type timedTx struct {
	base driver.Tx
	conn *timedConn
}

func (t *timedTx) finish(err error) error {
	started, first := t.conn.txStarted, t.conn.txFirstSQL
	t.conn.txStarted, t.conn.txFirstSQL = time.Time{}, ""
	if first == "" {
		first = "(no statements)"
	}
	logIfSlow("transaction", first, started)
	return err
}

func (t *timedTx) Commit() error   { return t.finish(t.base.Commit()) }
func (t *timedTx) Rollback() error { return t.finish(t.base.Rollback()) }

type timedStmt struct {
	base  driver.Stmt
	conn  *timedConn
	query string
}

func (s *timedStmt) Close() error  { return s.base.Close() }
func (s *timedStmt) NumInput() int { return s.base.NumInput() }

func (s *timedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("sqlite statements require ExecContext")
}

func (s *timedStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("sqlite statements require QueryContext")
}

func (s *timedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	execer, ok := s.base.(driver.StmtExecContext)
	if !ok {
		return nil, errors.New("sqlite statement does not support ExecContext")
	}
	s.conn.noteStatement(s.query)
	started := time.Now()
	result, err := execer.ExecContext(ctx, args)
	logIfSlow("statement", s.query, started)
	return result, err
}

func (s *timedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := s.base.(driver.StmtQueryContext)
	if !ok {
		return nil, errors.New("sqlite statement does not support QueryContext")
	}
	s.conn.noteStatement(s.query)
	started := time.Now()
	rows, err := queryer.QueryContext(ctx, args)
	if err != nil {
		logIfSlow("query", s.query, started)
		return nil, err
	}
	return &timedRows{base: rows, query: s.query, started: started}, nil
}

type timedRows struct {
	base    driver.Rows
	query   string
	started time.Time
	closed  bool
}

func (r *timedRows) Columns() []string { return r.base.Columns() }

func (r *timedRows) Next(dest []driver.Value) error {
	err := r.base.Next(dest)
	if err == io.EOF && !r.closed {
		// Rows are often read to the end long before Close is deferred.
		r.closed = true
		logIfSlow("query", r.query, r.started)
	}
	return err
}

func (r *timedRows) Close() error {
	if !r.closed {
		r.closed = true
		logIfSlow("query", r.query, r.started)
	}
	return r.base.Close()
}
