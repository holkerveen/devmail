package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Message is the stored form of a trapped email: envelope metadata plus the
// raw bytes as received. Nothing is parsed at receive time except Subject,
// which the list has to show. Bodies, headers and attachments are produced on
// demand by parse.go, which is what keeps a full ring down to its raw size
// instead of double-counting a decoded copy of every message.
type Message struct {
	ID         string    `json:"id"`
	ReceivedAt time.Time `json:"receivedAt"`
	From       string    `json:"from"` // envelope MAIL FROM
	To         []string  `json:"to"`   // envelope RCPT TO
	Subject    string    `json:"subject"`
	Size       int       `json:"size"`
	Raw        []byte    `json:"-"`
}

// Store is a mutex-guarded ring bounded by BOTH a message count and a total
// byte budget. Bounding by count alone lets a handful of large messages exhaust
// memory; bounding by bytes alone lets a flood of tiny ones grow the slice
// without limit.
//
// msgs is []*Message rather than []Message deliberately: dropping a slice
// element while a reader still holds the pointer is safe in Go (the object
// simply stays alive), whereas a []Message ring overwritten in place would tear
// under a concurrent reader.
type Store struct {
	mu       sync.RWMutex
	msgs     []*Message
	bytes    int64
	maxMsgs  int
	maxBytes int64

	// evicted counts messages dropped to stay within budget, so the operator
	// can tell "my mail never arrived" from "my mail arrived and aged out".
	evicted int64
}

func NewStore(maxMsgs int, maxBytes int64) *Store {
	return &Store{maxMsgs: maxMsgs, maxBytes: maxBytes}
}

// Add assigns an opaque ID and appends, evicting oldest until both bounds hold.
//
// The ID is random rather than a counter: decimal-string ids sort
// lexicographically wrong at ten messages ("10" < "2"), and a per-process
// counter makes a bookmarked /?id=5 point at unrelated mail after a restart.
func (s *Store) Add(m *Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m.ID = newID()
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = time.Now().UTC()
	}
	m.Size = len(m.Raw)

	s.msgs = append(s.msgs, m)
	s.bytes += int64(m.Size)

	// Evict oldest first. The guard on len(s.msgs) > 1 keeps a single
	// oversized message in the ring rather than evicting it to nothing --
	// config validation already refuses MaxMessageBytes > MaxTotalBytes, so
	// this only bites if the budget is changed under a running store.
	for len(s.msgs) > s.maxMsgs || (s.bytes > s.maxBytes && len(s.msgs) > 1) {
		s.bytes -= int64(s.msgs[0].Size)
		s.msgs[0] = nil // let the raw bytes go before the slice header moves
		s.msgs = s.msgs[1:]
		s.evicted++
	}
}

// List returns newest-first VALUES with Raw nil. Values, not pointers: handing
// out *Message would let a caller mutate the store, and would drag every
// message's raw bytes into the JSON encoder for the list endpoint.
//
// Insertion order under the mutex is the total order. Nothing sorts by
// ReceivedAt -- that would reintroduce a tie between messages arriving in the
// same nanosecond, which insertion order does not have.
func (s *Store) List() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Message, 0, len(s.msgs))
	for i := len(s.msgs) - 1; i >= 0; i-- {
		m := *s.msgs[i]
		m.Raw = nil
		out = append(out, m)
	}
	return out
}

func (s *Store) Get(id string) (*Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.msgs {
		if m.ID == id {
			return m, true
		}
	}
	return nil, false
}

func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.msgs {
		if m.ID == id {
			s.bytes -= int64(m.Size)
			s.msgs = append(s.msgs[:i], s.msgs[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = nil
	s.bytes = 0
}

// Stats reports the live counters, for /healthz and for logging.
func (s *Store) Stats() (count int, bytes int64, evicted int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.msgs), s.bytes, s.evicted
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable and not worth a degraded
		// id scheme; the process is unusable anyway.
		panic("devmail: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
