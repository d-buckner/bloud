package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Session configuration
	sessionPrefix     = "bloud:session:"
	defaultSessionTTL = 7 * 24 * time.Hour // 7 days
)

// Session represents an authenticated user session
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SessionStore manages sessions in Redis
type SessionStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewSessionStore creates a new Redis-backed session store
func NewSessionStore(redisAddr string) (*SessionStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &SessionStore{
		client: client,
		ttl:    defaultSessionTTL,
	}, nil
}

// Create creates a new session for a user
func (s *SessionStore) Create(ctx context.Context, userID string, username string) (*Session, error) {
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session ID: %w", err)
	}

	now := time.Now()
	session := &Session{
		ID:        sessionID,
		UserID:    userID,
		Username:  username,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}

	data, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	key := sessionPrefix + sessionID
	if err := s.client.Set(ctx, key, data, s.ttl).Err(); err != nil {
		return nil, fmt.Errorf("failed to store session: %w", err)
	}

	return session, nil
}

// Get retrieves a session by ID
func (s *SessionStore) Get(ctx context.Context, sessionID string) (*Session, error) {
	key := sessionPrefix + sessionID
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // Session not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	return &session, nil
}

// Delete removes a session
func (s *SessionStore) Delete(ctx context.Context, sessionID string) error {
	key := sessionPrefix + sessionID
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

// DeleteByUserID removes all sessions for a user
func (s *SessionStore) DeleteByUserID(ctx context.Context, userID string) error {
	// Scan for all sessions and delete those belonging to this user
	// This is O(n) but acceptable for session counts we expect
	var cursor uint64
	for {
		keys, nextCursor, err := s.client.Scan(ctx, cursor, sessionPrefix+"*", 100).Result()
		if err != nil {
			return fmt.Errorf("failed to scan sessions: %w", err)
		}

		for _, key := range keys {
			data, err := s.client.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}

			var session Session
			if err := json.Unmarshal(data, &session); err != nil {
				continue
			}

			if session.UserID == userID {
				s.client.Del(ctx, key)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// Refresh extends a session's TTL
func (s *SessionStore) Refresh(ctx context.Context, sessionID string) error {
	key := sessionPrefix + sessionID

	// Get current session
	data, err := s.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return fmt.Errorf("session not found")
	}
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Update expiry
	session.ExpiresAt = time.Now().Add(s.ttl)
	newData, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	if err := s.client.Set(ctx, key, newData, s.ttl).Err(); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}

// Close closes the Redis connection
func (s *SessionStore) Close() error {
	return s.client.Close()
}

// generateSessionID creates a cryptographically secure random session ID
func generateSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
