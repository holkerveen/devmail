package main

import (
	"strconv"
	"sync"
	"testing"
)

func TestAddAssignsNonEmptyUniqueIDs(t *testing.T) {
	s := NewStore(100, 1<<20)

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		m := &Message{Raw: []byte("body")}
		s.Add(m)
		if m.ID == "" {
			t.Fatalf("message %d got an empty ID", i)
		}
		if seen[m.ID] {
			t.Fatalf("message %d got a duplicate ID %q", i, m.ID)
		}
		seen[m.ID] = true
	}
}

func TestListReturnsNewestFirstWithRawNil(t *testing.T) {
	s := NewStore(100, 1<<20)
	var ids []string
	for i := 0; i < 3; i++ {
		m := &Message{Raw: []byte("body-" + strconv.Itoa(i))}
		s.Add(m)
		ids = append(ids, m.ID)
	}

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("List() returned %d messages, want 3", len(list))
	}
	for i, m := range list {
		wantID := ids[len(ids)-1-i]
		if m.ID != wantID {
			t.Errorf("List()[%d].ID = %q, want %q (newest first)", i, m.ID, wantID)
		}
		if m.Raw != nil {
			t.Errorf("List()[%d].Raw = %v, want nil", i, m.Raw)
		}
	}
}

func TestListReturnsValuesNotPointersIntoStore(t *testing.T) {
	s := NewStore(100, 1<<20)
	m := &Message{From: "original@example.com", Raw: []byte("body")}
	s.Add(m)

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d messages, want 1", len(list))
	}
	list[0].From = "mutated@example.com"

	got, ok := s.Get(m.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", m.ID)
	}
	if got.From != "original@example.com" {
		t.Errorf("stored message From = %q after mutating the List() copy, want unchanged %q", got.From, "original@example.com")
	}
}

func TestEvictionByCount(t *testing.T) {
	s := NewStore(3, 1<<20)
	var ids []string
	for i := 0; i < 5; i++ {
		m := &Message{Raw: []byte("x")}
		s.Add(m)
		ids = append(ids, m.ID)
	}

	count, _, evicted := s.Stats()
	if count != 3 {
		t.Errorf("Stats() count = %d, want 3", count)
	}
	if evicted != 2 {
		t.Errorf("Stats() evicted = %d, want 2", evicted)
	}

	// The three newest (ids[2], ids[3], ids[4]) should remain.
	for i, id := range ids {
		_, ok := s.Get(id)
		wantPresent := i >= 2
		if ok != wantPresent {
			t.Errorf("Get(ids[%d]) present = %v, want %v", i, ok, wantPresent)
		}
	}
}

func TestEvictionByBytesIsExact(t *testing.T) {
	// Budget for exactly two 10-byte messages.
	s := NewStore(100, 20)

	sizes := []int{10, 10, 10, 10}
	var ids []string
	for _, sz := range sizes {
		m := &Message{Raw: make([]byte, sz)}
		s.Add(m)
		ids = append(ids, m.ID)
	}

	count, bytes, evicted := s.Stats()
	if count != 2 {
		t.Fatalf("Stats() count = %d, want 2", count)
	}
	if bytes != 20 {
		t.Errorf("Stats() bytes = %d, want exactly 20", bytes)
	}
	if evicted != 2 {
		t.Errorf("Stats() evicted = %d, want 2", evicted)
	}
	// Only the last two messages should survive.
	if _, ok := s.Get(ids[0]); ok {
		t.Error("oldest message should have been evicted by byte budget")
	}
	if _, ok := s.Get(ids[1]); ok {
		t.Error("second-oldest message should have been evicted by byte budget")
	}
	if _, ok := s.Get(ids[2]); !ok {
		t.Error("third message should still be present")
	}
	if _, ok := s.Get(ids[3]); !ok {
		t.Error("newest message should still be present")
	}
}

func TestDeletePresentAndAbsent(t *testing.T) {
	s := NewStore(100, 1<<20)
	m := &Message{Raw: make([]byte, 42)}
	s.Add(m)

	if ok := s.Delete("does-not-exist"); ok {
		t.Error("Delete() of an absent id returned true")
	}
	_, initialBytes, _ := s.Stats()
	if initialBytes != 42 {
		t.Fatalf("precondition failed: bytes = %d, want 42", initialBytes)
	}

	if ok := s.Delete(m.ID); !ok {
		t.Fatal("Delete() of a present id returned false")
	}
	count, bytes, _ := s.Stats()
	if count != 0 {
		t.Errorf("Stats() count after delete = %d, want 0", count)
	}
	if bytes != 0 {
		t.Errorf("Stats() bytes after delete = %d, want 0", bytes)
	}

	// Deleting again should now report absent and change nothing.
	if ok := s.Delete(m.ID); ok {
		t.Error("Delete() of an already-deleted id returned true")
	}
}

func TestClearEmptiesMessagesAndBytes(t *testing.T) {
	s := NewStore(100, 1<<20)
	for i := 0; i < 3; i++ {
		s.Add(&Message{Raw: make([]byte, 10)})
	}

	s.Clear()

	count, bytes, _ := s.Stats()
	if count != 0 {
		t.Errorf("Stats() count after Clear() = %d, want 0", count)
	}
	if bytes != 0 {
		t.Errorf("Stats() bytes after Clear() = %d, want 0", bytes)
	}
	if len(s.List()) != 0 {
		t.Errorf("List() after Clear() = %v, want empty", s.List())
	}
}

// TestConcurrentAccessDoesNotRaceOrDeadlock exercises Add/List/Get/Delete from
// several goroutines at once. Run with -race; it exists to be run with -race.
func TestConcurrentAccessDoesNotRaceOrDeadlock(t *testing.T) {
	s := NewStore(50, 1<<16)

	const workers = 8
	const iterations = 50

	idsCh := make(chan string, workers*iterations)

	var producers sync.WaitGroup
	producers.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer producers.Done()
			for i := 0; i < iterations; i++ {
				m := &Message{Raw: []byte("payload")}
				s.Add(m)
				idsCh <- m.ID
				_ = s.List()
				s.Get(m.ID)
			}
		}()
	}

	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for i := 0; i < iterations; i++ {
			s.Stats()
		}
	}()

	// Delete concurrently with everything else until the producers finish and
	// idsCh is drained and closed.
	var deleter sync.WaitGroup
	deleter.Add(1)
	go func() {
		defer deleter.Done()
		for id := range idsCh {
			s.Delete(id)
		}
	}()

	producers.Wait()
	close(idsCh)
	readers.Wait()
	deleter.Wait()
}
