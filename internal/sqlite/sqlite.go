package sqlite

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>

static int exec_stmt(sqlite3 *db, const char *sql, char **errmsg) {
	return sqlite3_exec(db, sql, NULL, NULL, errmsg);
}
*/
import "C"

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unsafe"
)

func init() {
	sql.Register("sqlite3", &Driver{})
}

// Error represents a SQLite error with its primary and extended result codes.
type Error struct {
	Code         int
	ExtendedCode int
	Msg          string
}

func (e *Error) Error() string {
	return fmt.Sprintf("sqlite error (%d/%d): %s", e.Code, e.ExtendedCode, e.Msg)
}

// IsConstraint returns true if the error is a SQLite constraint violation.
func IsConstraint(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.Code == int(C.SQLITE_CONSTRAINT)
	}
	return false
}

// Driver implements database/sql/driver.Driver using libsqlite3 via CGO.
type Driver struct{}

// Open opens a SQLite database connection.
func (d *Driver) Open(dsn string) (driver.Conn, error) {
	cDSN := C.CString(dsn)
	defer C.free(unsafe.Pointer(cDSN))

	var db *C.sqlite3
	flags := C.SQLITE_OPEN_READWRITE | C.SQLITE_OPEN_CREATE | C.SQLITE_OPEN_FULLMUTEX | C.SQLITE_OPEN_URI
	rc := C.sqlite3_open_v2(cDSN, &db, C.int(flags), nil)
	if rc != C.SQLITE_OK {
		err := connErr(db, rc)
		if db != nil {
			C.sqlite3_close_v2(db)
		}
		return nil, err
	}

	C.sqlite3_busy_timeout(db, 5000)
	return &Conn{db: db}, nil
}

// Conn implements database/sql/driver.Conn and related interfaces.
type Conn struct {
	mu sync.Mutex
	db *C.sqlite3
}

func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	rc := C.sqlite3_close_v2(c.db)
	c.db = nil
	if rc != C.SQLITE_OK {
		return fmt.Errorf("sqlite close failed: %d", rc)
	}
	return nil
}

func (c *Conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *Conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE", nil); err != nil {
		return nil, err
	}
	return &Tx{conn: c}, nil
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *Conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil, driver.ErrBadConn
	}

	cSQL := C.CString(query)
	defer C.free(unsafe.Pointer(cSQL))

	var stmt *C.sqlite3_stmt
	var tail *C.char
	rc := C.sqlite3_prepare_v2(c.db, cSQL, -1, &stmt, &tail)
	if rc != C.SQLITE_OK {
		return nil, connErr(c.db, rc)
	}
	if tail != nil && strings.TrimSpace(C.GoString(tail)) != "" {
		// Multi-statement prepare is handled by ExecContext when needed; single Stmt only supports one statement.
		C.sqlite3_finalize(stmt)
		return nil, errors.New("sqlite: multiple statements not supported in Prepare")
	}
	return &Stmt{conn: c, stmt: stmt}, nil
}

func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.db == nil {
			return nil, driver.ErrBadConn
		}
		cSQL := C.CString(query)
		defer C.free(unsafe.Pointer(cSQL))
		var errmsg *C.char
		rc := C.exec_stmt(c.db, cSQL, &errmsg)
		if errmsg != nil {
			msg := C.GoString(errmsg)
			C.sqlite3_free(unsafe.Pointer(errmsg))
			return nil, &Error{
				Code:         int(rc),
				ExtendedCode: int(C.sqlite3_extended_errcode(c.db)),
				Msg:          msg,
			}
		}
		if rc != C.SQLITE_OK {
			return nil, connErr(c.db, rc)
		}
		return &Result{
			lastInsertID: int64(C.sqlite3_last_insert_rowid(c.db)),
			rowsAffected: int64(C.sqlite3_changes(c.db)),
		}, nil
	}

	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.(*Stmt).ExecContext(ctx, args)
}

func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stmt, err := c.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	rows, err := stmt.(*Stmt).QueryContext(ctx, args)
	if err != nil {
		_ = stmt.Close()
		return nil, err
	}
	rows.(*Rows).closeStmt = true
	return rows, nil
}

func (c *Conn) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return driver.ErrBadConn
	}
	cSQL := C.CString("SELECT 1")
	defer C.free(unsafe.Pointer(cSQL))
	var errmsg *C.char
	rc := C.exec_stmt(c.db, cSQL, &errmsg)
	if errmsg != nil {
		C.sqlite3_free(unsafe.Pointer(errmsg))
	}
	if rc != C.SQLITE_OK {
		return connErr(c.db, rc)
	}
	return nil
}

// Tx implements database/sql/driver.Tx.
type Tx struct {
	conn *Conn
}

func (t *Tx) Commit() error {
	_, err := t.conn.ExecContext(context.Background(), "COMMIT", nil)
	return err
}

func (t *Tx) Rollback() error {
	_, err := t.conn.ExecContext(context.Background(), "ROLLBACK", nil)
	return err
}

// Stmt implements database/sql/driver.Stmt.
type Stmt struct {
	conn *Conn
	stmt *C.sqlite3_stmt
}

func (s *Stmt) Close() error {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	if s.stmt == nil {
		return nil
	}
	C.sqlite3_finalize(s.stmt)
	s.stmt = nil
	return nil
}

func (s *Stmt) NumInput() int {
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	if s.stmt == nil {
		return -1
	}
	return int(C.sqlite3_bind_parameter_count(s.stmt))
}

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), valuesToNamed(args))
}

func (s *Stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	if s.conn.db == nil || s.stmt == nil {
		return nil, driver.ErrBadConn
	}
	if err := bindArgs(s.conn.db, s.stmt, args); err != nil {
		return nil, err
	}
	rc := C.sqlite3_step(s.stmt)
	if rc != C.SQLITE_DONE && rc != C.SQLITE_ROW {
		err := connErr(s.conn.db, rc)
		C.sqlite3_reset(s.stmt)
		return nil, err
	}
	res := &Result{
		lastInsertID: int64(C.sqlite3_last_insert_rowid(s.conn.db)),
		rowsAffected: int64(C.sqlite3_changes(s.conn.db)),
	}
	C.sqlite3_reset(s.stmt)
	return res, nil
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), valuesToNamed(args))
}

func (s *Stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.conn.mu.Lock()
	defer s.conn.mu.Unlock()
	if s.conn.db == nil || s.stmt == nil {
		return nil, driver.ErrBadConn
	}
	if err := bindArgs(s.conn.db, s.stmt, args); err != nil {
		return nil, err
	}
	colCount := int(C.sqlite3_column_count(s.stmt))
	cols := make([]string, colCount)
	for i := 0; i < colCount; i++ {
		cols[i] = C.GoString(C.sqlite3_column_name(s.stmt, C.int(i)))
	}
	return &Rows{stmt: s, cols: cols}, nil
}

// Result implements database/sql/driver.Result.
type Result struct {
	lastInsertID int64
	rowsAffected int64
}

func (r *Result) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r *Result) RowsAffected() (int64, error) { return r.rowsAffected, nil }

// Rows implements database/sql/driver.Rows.
type Rows struct {
	stmt      *Stmt
	cols      []string
	closeStmt bool
	closed    bool
}

func (r *Rows) Columns() []string { return r.cols }

func (r *Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.stmt != nil && r.stmt.stmt != nil {
		r.stmt.conn.mu.Lock()
		C.sqlite3_reset(r.stmt.stmt)
		r.stmt.conn.mu.Unlock()
	}
	if r.closeStmt && r.stmt != nil {
		return r.stmt.Close()
	}
	return nil
}

func (r *Rows) Next(dest []driver.Value) error {
	if r.closed || r.stmt == nil || r.stmt.stmt == nil {
		return io.EOF
	}
	r.stmt.conn.mu.Lock()
	defer r.stmt.conn.mu.Unlock()

	rc := C.sqlite3_step(r.stmt.stmt)
	if rc == C.SQLITE_DONE {
		return io.EOF
	}
	if rc != C.SQLITE_ROW {
		return connErr(r.stmt.conn.db, rc)
	}

	for i := range dest {
		switch C.sqlite3_column_type(r.stmt.stmt, C.int(i)) {
		case C.SQLITE_NULL:
			dest[i] = nil
		case C.SQLITE_INTEGER:
			dest[i] = int64(C.sqlite3_column_int64(r.stmt.stmt, C.int(i)))
		case C.SQLITE_FLOAT:
			dest[i] = float64(C.sqlite3_column_double(r.stmt.stmt, C.int(i)))
		case C.SQLITE_BLOB:
			n := int(C.sqlite3_column_bytes(r.stmt.stmt, C.int(i)))
			if n == 0 {
				dest[i] = []byte{}
			} else {
				ptr := C.sqlite3_column_blob(r.stmt.stmt, C.int(i))
				dest[i] = C.GoBytes(ptr, C.int(n))
			}
		case C.SQLITE_TEXT:
			n := int(C.sqlite3_column_bytes(r.stmt.stmt, C.int(i)))
			ptr := unsafe.Pointer(C.sqlite3_column_text(r.stmt.stmt, C.int(i)))
			dest[i] = C.GoStringN((*C.char)(ptr), C.int(n))
		}
	}
	return nil
}

func bindArgs(db *C.sqlite3, stmt *C.sqlite3_stmt, args []driver.NamedValue) error {
	C.sqlite3_reset(stmt)
	C.sqlite3_clear_bindings(stmt)
	for _, arg := range args {
		idx := C.int(arg.Ordinal)
		if arg.Name != "" {
			cname := C.CString(":" + arg.Name)
			idx = C.sqlite3_bind_parameter_index(stmt, cname)
			C.free(unsafe.Pointer(cname))
		}
		var rc C.int
		switch v := arg.Value.(type) {
		case nil:
			rc = C.sqlite3_bind_null(stmt, idx)
		case int64:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case float64:
			rc = C.sqlite3_bind_double(stmt, idx, C.double(v))
		case bool:
			if v {
				rc = C.sqlite3_bind_int64(stmt, idx, 1)
			} else {
				rc = C.sqlite3_bind_int64(stmt, idx, 0)
			}
		case string:
			cstr := C.CString(v)
			rc = C.sqlite3_bind_text(stmt, idx, cstr, C.int(len(v)), C.SQLITE_TRANSIENT)
			C.free(unsafe.Pointer(cstr))
		case []byte:
			if len(v) == 0 {
				rc = C.sqlite3_bind_blob(stmt, idx, nil, 0, C.SQLITE_TRANSIENT)
			} else {
				rc = C.sqlite3_bind_blob(stmt, idx, unsafe.Pointer(&v[0]), C.int(len(v)), C.SQLITE_TRANSIENT)
			}
		default:
			return fmt.Errorf("sqlite: unsupported bind type %T", arg.Value)
		}
		if rc != C.SQLITE_OK {
			return connErr(db, rc)
		}
	}
	return nil
}

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

func connErr(db *C.sqlite3, rc C.int) error {
	if db == nil {
		return &Error{Code: int(rc), Msg: "database handle is nil"}
	}
	return &Error{
		Code:         int(rc),
		ExtendedCode: int(C.sqlite3_extended_errcode(db)),
		Msg:          C.GoString(C.sqlite3_errmsg(db)),
	}
}
