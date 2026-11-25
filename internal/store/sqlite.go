package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/shorty/shorty/internal/shortcode"
	"github.com/shorty/shorty/internal/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var (
	ErrNotFound          = errors.New("short code not found")
	ErrCodeCollision     = errors.New("failed to allocate unique short code")
	ErrCodeAlreadyExists = errors.New("short code already exists")
)

const maxCreateAttempts = 8

// Link represents a stored short link mapping.
type Link struct {
	Code        string
	Destination string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Store persists short links in SQLite.
type Store struct {
	db         *sql.DB
	codeLength int
	retention  time.Duration
}

// Open opens (or creates) the SQLite database at dbPath and applies pending migrations.
func Open(ctx context.Context, dbPath string, codeLength int, retention time.Duration) (*Store, error) {
	if dbPath == "" {
		return nil, errors.New("database path must not be empty")
	}
	if codeLength < shortcode.MinLength || codeLength > shortcode.MaxLength {
		codeLength = shortcode.DefaultLength
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles concurrent readers well in WAL mode, and a single writer
	// connection avoids SQLITE_BUSY contention inside the process.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
	}

	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	return &Store{
		db:         db,
		codeLength: codeLength,
		retention:  retention,
	}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping checks whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Create generates a unique random code and stores destination.
// It retries on primary-key collision up to maxCreateAttempts times.
func (s *Store) Create(ctx context.Context, destination string) (*Link, error) {
	now := time.Now().UTC()
	var expiresAtUnix int64
	var expiresAt time.Time
	if s.retention > 0 {
		expiresAt = now.Add(s.retention)
		expiresAtUnix = expiresAt.UnixNano()
	}

	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		code, err := shortcode.Generate(s.codeLength)
		if err != nil {
			return nil, fmt.Errorf("generate code: %w", err)
		}
		err = s.insert(ctx, code, destination, now.UnixNano(), expiresAtUnix)
		if err == nil {
			return &Link{
				Code:        code,
				Destination: destination,
				CreatedAt:   now,
				ExpiresAt:   expiresAt,
			}, nil
		}
		if errors.Is(err, ErrCodeAlreadyExists) {
			continue
		}
		return nil, err
	}
	return nil, ErrCodeCollision
}

// InsertWithCode inserts a specific code; primarily used by tests to verify collision handling.
func (s *Store) InsertWithCode(ctx context.Context, code, destination string) error {
	now := time.Now().UTC()
	var expiresAtUnix int64
	if s.retention > 0 {
		expiresAtUnix = now.Add(s.retention).UnixNano()
	}
	return s.insert(ctx, code, destination, now.UnixNano(), expiresAtUnix)
}

func (s *Store) insert(ctx context.Context, code, destination string, createdAtUnix, expiresAtUnix int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO links (code, destination, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		code, destination, createdAtUnix, expiresAtUnix,
	)
	if err != nil {
		if sqlite.IsConstraint(err) {
			return ErrCodeAlreadyExists
		}
		return fmt.Errorf("insert link: %w", err)
	}
	return nil
}

// Resolve looks up a non-expired link by code.
func (s *Store) Resolve(ctx context.Context, code string) (*Link, error) {
	var dest string
	var createdUnix, expiresUnix int64
	err := s.db.QueryRowContext(ctx,
		`SELECT destination, created_at, expires_at FROM links WHERE code = ?`,
		code,
	).Scan(&dest, &createdUnix, &expiresUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query link: %w", err)
	}

	now := time.Now().UTC()
	if expiresUnix > 0 && now.UnixNano() >= expiresUnix {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM links WHERE code = ?`, code)
		return nil, ErrNotFound
	}

	var expiresAt time.Time
	if expiresUnix > 0 {
		expiresAt = time.Unix(0, expiresUnix).UTC()
	}
	return &Link{
		Code:        code,
		Destination: dest,
		CreatedAt:   time.Unix(0, createdUnix).UTC(),
		ExpiresAt:   expiresAt,
	}, nil
}

// Delete removes a link by code. Returns ErrNotFound if no row was deleted.
func (s *Store) Delete(ctx context.Context, code string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneExpired deletes all expired links and returns the number of removed rows.
func (s *Store) PruneExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM links WHERE expires_at > 0 AND expires_at <= ?`,
		now.UTC().UnixNano(),
	)
	if err != nil {
		return 0, fmt.Errorf("prune expired links: %w", err)
	}
	return res.RowsAffected()
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, name).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}

		content, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, name, time.Now().UTC().UnixNano()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
