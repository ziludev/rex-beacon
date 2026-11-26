package notify

import (
	"sync"
	"time"
)

// SentMessageRef links a sent chat message ID back to a Rex terminal pane.
type SentMessageRef struct {
	MessageID    string
	BlockID      string
	SessionID    string
	SessionLabel string
	App          string
	CreatedAt    time.Time
}

// RefStore remembers recent sent message IDs and block IDs in a bounded ring buffer
// so interactive replies and button callbacks can resolve target Rex panes quickly.
type RefStore struct {
	mu      sync.RWMutex
	max     int
	order   []string
	byMsg   map[string]SentMessageRef
	byShort map[string]string
}

// NewRefStore creates a RefStore keeping up to maxEntries recent references.
func NewRefStore(maxEntries int) *RefStore {
	if maxEntries <= 0 {
		maxEntries = 500
	}
	return &RefStore{
		max:     maxEntries,
		byMsg:   make(map[string]SentMessageRef, maxEntries),
		byShort: make(map[string]string, maxEntries),
	}
}

// Record stores messageID -> notification pane metadata.
func (s *RefStore) Record(messageID string, n Notification) {
	if s == nil || n.BlockID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if short := ShortID(n.BlockID); short != "" {
		s.byShort[short] = n.BlockID
	}
	if messageID == "" {
		return
	}
	if _, exists := s.byMsg[messageID]; !exists {
		if len(s.order) >= s.max {
			evict := s.order[0]
			s.order = s.order[1:]
			delete(s.byMsg, evict)
		}
		s.order = append(s.order, messageID)
	}
	s.byMsg[messageID] = SentMessageRef{
		MessageID:    messageID,
		BlockID:      n.BlockID,
		SessionID:    n.SessionID,
		SessionLabel: n.SessionLabel,
		App:          n.App,
		CreatedAt:    time.Now(),
	}
}

// LookupMessage resolves a sent message ID to its originating Rex pane.
func (s *RefStore) LookupMessage(messageID string) (SentMessageRef, bool) {
	if s == nil || messageID == "" {
		return SentMessageRef{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.byMsg[messageID]
	return ref, ok
}

// LookupShortBlock resolves a short block ID suffix back to the full block ID.
func (s *RefStore) LookupShortBlock(short string) (string, bool) {
	if s == nil || short == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byShort[short]
	return id, ok
}
