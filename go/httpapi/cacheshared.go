// A cache several machines and successive deploys can share.
//
// The in-memory store answers from one machine's own memory and starts
// empty after every deploy, so a first ask pays the provider cost again
// however often the same question has already been answered. Measured
// 2026-09-06 across all 55 routes, a cold ask took a median 5,256ms
// against 69ms once cached: the gap between a shared cache and no shared
// cache is most of this server's latency story.
//
// Two backends, one for each way this server runs. Upstash speaks HTTP,
// which suits a deployment with no volume to mount and machines that come
// and go. A local Redis speaks RESP over TCP, which suits anyone running
// this themselves. Both are reached with nothing but the standard
// library, so neither costs this module a dependency.
package httpapi

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// sharedCacheTimeout bounds a cache lookup. A cache is an optimisation,
// so waiting on a slow one longer than the work it saves defeats it.
const sharedCacheTimeout = 2 * time.Second

// storedEntry is a cacheEntry on the wire. cacheEntry's own fields are
// unexported, so it carries no JSON representation of its own.
type storedEntry struct {
	Body        []byte `json:"body"`
	ContentType string `json:"content_type"`
	Status      int    `json:"status"`
	ExpiresAt   int64  `json:"expires_at"`
}

func encodeEntry(entry cacheEntry) ([]byte, error) {
	return json.Marshal(storedEntry{
		Body:        entry.body,
		ContentType: entry.contentType,
		Status:      entry.status,
		ExpiresAt:   entry.expiresAt.Unix(),
	})
}

func decodeEntry(raw []byte) (cacheEntry, bool) {
	var stored storedEntry
	if err := json.Unmarshal(raw, &stored); err != nil {
		return cacheEntry{}, false
	}
	return cacheEntry{
		body:        stored.Body,
		contentType: stored.ContentType,
		status:      stored.Status,
		expiresAt:   time.Unix(stored.ExpiresAt, 0),
	}, true
}

// sharedKey is what a shared backend is keyed on. The local key embeds
// the caller's Monid API key, which must not leave this process, so it is
// hashed rather than sent. The hash also keeps the key printable, which a
// URL path segment requires.
func sharedKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "fdcache:" + hex.EncodeToString(sum[:])
}

// secondsUntil is the TTL a backend expires an entry after. An entry
// already past its expiry is not worth storing.
func secondsUntil(expiresAt time.Time, now time.Time) (int, bool) {
	seconds := int(expiresAt.Sub(now).Seconds())
	if seconds < 1 {
		return 0, false
	}
	return seconds, true
}

// --- Upstash, over its HTTP API ---

// upstashStore reads and writes one Upstash Redis database over HTTPS.
type upstashStore struct {
	base   string
	token  string
	client *http.Client
}

func newUpstashStore(base, token string) *upstashStore {
	return &upstashStore{
		base:   strings.TrimSuffix(base, "/"),
		token:  token,
		client: &http.Client{Timeout: sharedCacheTimeout},
	}
}

func (s *upstashStore) get(key string, now time.Time) (cacheEntry, bool) {
	req, err := http.NewRequest(http.MethodGet, s.base+"/get/"+url.PathEscape(sharedKey(key)), nil)
	if err != nil {
		return cacheEntry{}, false
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return cacheEntry{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cacheEntry{}, false
	}
	// Upstash answers {"result": "<value>"}, and null for a key it does
	// not hold.
	var envelope struct {
		Result *string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || envelope.Result == nil {
		return cacheEntry{}, false
	}
	entry, ok := decodeEntry([]byte(*envelope.Result))
	if !ok || now.After(entry.expiresAt) {
		return cacheEntry{}, false
	}
	return entry, true
}

func (s *upstashStore) put(key string, entry cacheEntry) {
	seconds, ok := secondsUntil(entry.expiresAt, time.Now())
	if !ok {
		return
	}
	payload, err := encodeEntry(entry)
	if err != nil {
		return
	}
	// The value travels as the request body, so a response of any size
	// stays out of the URL.
	target := fmt.Sprintf("%s/set/%s?EX=%d", s.base, url.PathEscape(sharedKey(key)), seconds)
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(string(payload)))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// --- A Redis anyone can run, over RESP ---

// redisStore speaks enough RESP to GET and SET with an expiry. It opens a
// connection per command: this server answers a request per cache
// lookup, not thousands, and a pool would be more machinery than the
// saving is worth.
type redisStore struct {
	address  string
	password string
}

func newRedisStore(address, password string) *redisStore {
	return &redisStore{address: address, password: password}
}

// command writes one RESP array and returns the reply's bulk string.
// A nil bulk string reports absent rather than empty.
func (s *redisStore) command(args ...string) (string, bool) {
	conn, err := net.DialTimeout("tcp", s.address, sharedCacheTimeout)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sharedCacheTimeout))

	reader := bufio.NewReader(conn)
	if s.password != "" {
		if _, ok := writeCommand(conn, reader, "AUTH", s.password); !ok {
			return "", false
		}
	}
	return writeCommand(conn, reader, args...)
}

// writeCommand sends one command and reads its reply.
func writeCommand(conn net.Conn, reader *bufio.Reader, args ...string) (string, bool) {
	var request strings.Builder
	fmt.Fprintf(&request, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&request, "$%d\r\n%s\r\n", len(arg), arg)
	}
	if _, err := io.WriteString(conn, request.String()); err != nil {
		return "", false
	}
	return readReply(reader)
}

// readReply reads one RESP reply. Only the four shapes these two
// commands can produce are understood; anything else is a miss.
func readReply(reader *bufio.Reader) (string, bool) {
	line, err := reader.ReadString('\n')
	if err != nil || len(line) < 3 {
		return "", false
	}
	body := strings.TrimRight(line[1:], "\r\n")
	switch line[0] {
	case '+': // simple string, e.g. OK
		return body, true
	case '-': // error
		return "", false
	case ':': // integer
		return body, true
	case '$': // bulk string, or -1 for a key that is not held
		length, err := strconv.Atoi(body)
		if err != nil || length < 0 {
			return "", false
		}
		payload := make([]byte, length+2) // the value plus its trailing CRLF
		if _, err := io.ReadFull(reader, payload); err != nil {
			return "", false
		}
		return string(payload[:length]), true
	default:
		return "", false
	}
}

func (s *redisStore) get(key string, now time.Time) (cacheEntry, bool) {
	value, ok := s.command("GET", sharedKey(key))
	if !ok {
		return cacheEntry{}, false
	}
	entry, ok := decodeEntry([]byte(value))
	if !ok || now.After(entry.expiresAt) {
		return cacheEntry{}, false
	}
	return entry, true
}

func (s *redisStore) put(key string, entry cacheEntry) {
	seconds, ok := secondsUntil(entry.expiresAt, time.Now())
	if !ok {
		return
	}
	payload, err := encodeEntry(entry)
	if err != nil {
		return
	}
	s.command("SET", sharedKey(key), string(payload), "EX", strconv.Itoa(seconds))
}

// --- Choosing one ---

// tieredStore answers from memory first and from the shared backend
// second, so a machine that has already served a response never pays the
// network for it again. A shared hit is written back into memory for the
// same reason.
type tieredStore struct {
	local  cacheStore
	shared cacheStore
}

func (s *tieredStore) get(key string, now time.Time) (cacheEntry, bool) {
	if entry, hit := s.local.get(key, now); hit {
		return entry, true
	}
	entry, hit := s.shared.get(key, now)
	if hit {
		s.local.put(key, entry)
	}
	return entry, hit
}

func (s *tieredStore) put(key string, entry cacheEntry) {
	s.local.put(key, entry)
	s.shared.put(key, entry)
}

// newCacheStore builds the cache from configuration. With no cache URL
// configured this server behaves exactly as it did before: one bounded
// store per machine, no network, nothing to run alongside it.
//
//	CACHE_URL=https://<db>.upstash.io  CACHE_TOKEN=<token>   Upstash
//	CACHE_URL=redis://[:password@]host:6379                  any Redis
//	CACHE_URL unset                                          in-memory
func newCacheStore(cacheURL, token string) cacheStore {
	local := newResponseCache()
	shared := sharedStoreFor(cacheURL, token)
	if shared == nil {
		return local
	}
	return &tieredStore{local: local, shared: shared}
}

// sharedStoreFor returns the shared backend a cache URL names, or nil
// when it names none this server can speak to.
func sharedStoreFor(cacheURL, token string) cacheStore {
	cacheURL = strings.TrimSpace(cacheURL)
	if cacheURL == "" {
		return nil
	}
	parsed, err := url.Parse(cacheURL)
	if err != nil {
		return nil
	}
	switch parsed.Scheme {
	case "https", "http":
		if token == "" {
			return nil
		}
		return newUpstashStore(cacheURL, token)
	case "redis", "rediss":
		address := parsed.Host
		if parsed.Port() == "" {
			address = net.JoinHostPort(address, "6379")
		}
		password, _ := parsed.User.Password()
		if password == "" {
			password = token
		}
		return newRedisStore(address, password)
	default:
		return nil
	}
}
