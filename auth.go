package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"sync"
	"time"
)

const (
	pbkdf2Iterations = 120000
	pbkdf2KeyLen     = 32
)

// pbkdf2SHA256 使用标准库实现 PBKDF2-HMAC-SHA256。
func pbkdf2SHA256(password string, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, []byte(password))
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	dk := make([]byte, 0, numBlocks*hashLen)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)
	var block [4]byte
	for i := 1; i <= numBlocks; i++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(block[:], uint32(i))
		prf.Write(block[:])
		u = prf.Sum(nil)
		copy(t, u)
		for j := 1; j < iter; j++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for k := 0; k < hashLen; k++ {
				t[k] ^= u[k]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// hashPassword 生成 (salt, hash)。
func hashPassword(password string) (string, string) {
	salt := randomBytes(16)
	hash := pbkdf2SHA256(password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	return base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(hash)
}

func verifyPassword(password, saltB64, hashB64 string) bool {
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	expect, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	got := pbkdf2SHA256(password, salt, pbkdf2Iterations, pbkdf2KeyLen)
	return subtle.ConstantTimeCompare(got, expect) == 1
}

// ---------------- 会话 ----------------

type Session struct {
	Token  string
	UserID int
	Expire time.Time
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{
		sessions: map[string]*Session{},
		ttl:      ttl,
	}
}

func (ss *SessionStore) Create(userID int) string {
	tokenBytes := randomBytes(24)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.sessions[token] = &Session{Token: token, UserID: userID, Expire: time.Now().Add(ss.ttl)}
	return token
}

func (ss *SessionStore) Get(token string) *Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.sessions[token]
	if !ok {
		return nil
	}
	if time.Now().After(s.Expire) {
		delete(ss.sessions, token)
		return nil
	}
	s.Expire = time.Now().Add(ss.ttl)
	return s
}

func (ss *SessionStore) Delete(token string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.sessions, token)
}

func (ss *SessionStore) Cleanup() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := time.Now()
	for k, s := range ss.sessions {
		if now.After(s.Expire) {
			delete(ss.sessions, k)
		}
	}
}
