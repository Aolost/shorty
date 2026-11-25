package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shorty/shorty/internal/shortcode"
)

func TestStore_CreateResolveDeleteAndRestartPersistence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "shorty.db")

	st, err := Open(ctx, dbPath, shortcode.DefaultLength, 0)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	link, err := st.Create(ctx, "https://example.com/hello")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if link.Code == "" || link.Destination != "https://example.com/hello" {
		t.Fatalf("unexpected link: %+v", link)
	}

	// Close and reopen to verify restart persistence.
	if err := st.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	st2, err := Open(ctx, dbPath, shortcode.DefaultLength, 0)
	if err != nil {
		t.Fatalf("Reopen failed: %v", err)
	}
	defer st2.Close()

	got, err := st2.Resolve(ctx, link.Code)
	if err != nil {
		t.Fatalf("Resolve after reopen failed: %v", err)
	}
	if got.Destination != link.Destination {
		t.Fatalf("got destination %q, want %q", got.Destination, link.Destination)
	}

	if err := st2.Delete(ctx, link.Code); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := st2.Delete(ctx, link.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on second delete, got %v", err)
	}
	if _, err := st2.Resolve(ctx, link.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestStore_CollisionDetection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "collision.db")

	st, err := Open(ctx, dbPath, shortcode.DefaultLength, 0)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	const code = "AbCdEfGh123"
	if err := st.InsertWithCode(ctx, code, "https://example.com/one"); err != nil {
		t.Fatalf("first insert failed: %v", err)
	}
	if err := st.InsertWithCode(ctx, code, "https://example.com/two"); !errors.Is(err, ErrCodeAlreadyExists) {
		t.Fatalf("expected ErrCodeAlreadyExists, got %v", err)
	}
}

func TestStore_ConcurrentCreation(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")

	st, err := Open(ctx, dbPath, shortcode.DefaultLength, 0)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	const workers = 16
	const perWorker = 25
	var wg sync.WaitGroup
	codes := make(chan string, workers*perWorker)
	errs := make(chan error, workers*perWorker)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				link, err := st.Create(ctx, "https://example.com/concurrent")
				if err != nil {
					errs <- err
					return
				}
				codes <- link.Code
			}
		}()
	}
	wg.Wait()
	close(codes)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Create failed: %v", err)
	}

	seen := make(map[string]struct{}, workers*perWorker)
	for c := range codes {
		if _, dup := seen[c]; dup {
			t.Fatalf("duplicate code created concurrently: %q", c)
		}
		seen[c] = struct{}{}
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("got %d codes, want %d", len(seen), workers*perWorker)
	}
}

func TestStore_RetentionAndPrune(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "retention.db")

	st, err := Open(ctx, dbPath, shortcode.DefaultLength, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	link, err := st.Create(ctx, "https://example.com/ephemeral")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := st.Resolve(ctx, link.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected expired link to return ErrNotFound, got %v", err)
	}

	link2, err := st.Create(ctx, "https://example.com/ephemeral2")
	if err != nil {
		t.Fatalf("Create 2 failed: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	pruned, err := st.PruneExpired(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("PruneExpired failed: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned %d rows, want 1", pruned)
	}
	if _, err := st.Resolve(ctx, link2.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected pruned link to return ErrNotFound, got %v", err)
	}
}
