package httpapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleEntry(body string, ttl time.Duration) cacheEntry {
	return cacheEntry{
		body:        []byte(body),
		contentType: "application/json",
		status:      http.StatusOK,
		expiresAt:   time.Now().Add(ttl),
	}
}

// TestUpstashStore_RoundTrip drives the store against a server speaking
// Upstash's REST shape, checking the value travels in the body rather
// than the URL and comes back through a real decode.
func TestUpstashStore_RoundTrip(t *testing.T) {
	var mu sync.Mutex
	held := map[string]string{}
	var lastSetPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/set/"):
			lastSetPath = r.URL.String()
			payload, _ := io.ReadAll(r.Body)
			held[strings.TrimPrefix(r.URL.Path, "/set/")] = string(payload)
			_, _ = w.Write([]byte(`{"result":"OK"}`))
		case strings.HasPrefix(r.URL.Path, "/get/"):
			value, ok := held[strings.TrimPrefix(r.URL.Path, "/get/")]
			if !ok {
				_, _ = w.Write([]byte(`{"result":null}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": value})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := newUpstashStore(server.URL, "token-123")
	if _, hit := store.get("absent", time.Now()); hit {
		t.Fatal("a key that was never stored reported a hit")
	}

	store.put("k|/financials?ticker=AAPL", sampleEntry(`{"ok":true}`, time.Minute))
	entry, hit := store.get("k|/financials?ticker=AAPL", time.Now())
	if !hit {
		t.Fatal("a stored entry did not come back")
	}
	if string(entry.body) != `{"ok":true}` || entry.contentType != "application/json" || entry.status != 200 {
		t.Fatalf("entry came back as %q %q %d", entry.body, entry.contentType, entry.status)
	}
	if !strings.Contains(lastSetPath, "EX=") {
		t.Fatalf("set path %q carries no expiry, so the backend would hold it forever", lastSetPath)
	}

	// The caller's Monid key is part of the local key and must not be
	// sent to a third party.
	mu.Lock()
	defer mu.Unlock()
	for key := range held {
		if strings.Contains(key, "AAPL") || strings.Contains(key, "k|") {
			t.Fatalf("the shared key %q leaks the local key it was built from", key)
		}
	}
}

// TestUpstashStore_UnreachableIsAMiss checks a broken backend costs a
// slower response and never an error.
func TestUpstashStore_UnreachableIsAMiss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := newUpstashStore(server.URL, "token")
	store.put("key", sampleEntry("body", time.Minute)) // must not panic
	if _, hit := store.get("key", time.Now()); hit {
		t.Fatal("a failing backend reported a hit")
	}
}

// fakeRedis serves RESP well enough to answer AUTH, SET and GET.
func fakeRedis(t *testing.T) (address string, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	held := map[string]string{}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					args, ok := readCommand(reader)
					if !ok {
						return
					}
					mu.Lock()
					switch strings.ToUpper(args[0]) {
					case "AUTH":
						_, _ = io.WriteString(conn, "+OK\r\n")
					case "SET":
						held[args[1]] = args[2]
						_, _ = io.WriteString(conn, "+OK\r\n")
					case "GET":
						if value, present := held[args[1]]; present {
							_, _ = io.WriteString(conn, "$"+itoa(len(value))+"\r\n"+value+"\r\n")
						} else {
							_, _ = io.WriteString(conn, "$-1\r\n")
						}
					default:
						_, _ = io.WriteString(conn, "-ERR unknown\r\n")
					}
					mu.Unlock()
				}
			}(conn)
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// readCommand reads one RESP array of bulk strings.
func readCommand(reader *bufio.Reader) ([]string, bool) {
	line, err := reader.ReadString('\n')
	if err != nil || len(line) < 2 || line[0] != '*' {
		return nil, false
	}
	count := 0
	for _, c := range strings.TrimRight(line[1:], "\r\n") {
		count = count*10 + int(c-'0')
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil || len(header) < 2 || header[0] != '$' {
			return nil, false
		}
		length := 0
		for _, c := range strings.TrimRight(header[1:], "\r\n") {
			length = length*10 + int(c-'0')
		}
		payload := make([]byte, length+2)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, false
		}
		args = append(args, string(payload[:length]))
	}
	return args, true
}

// TestRedisStore_RoundTrip drives the RESP codec against a real socket,
// including the nil bulk string a Redis answers for a key it does not
// hold.
func TestRedisStore_RoundTrip(t *testing.T) {
	address, stop := fakeRedis(t)
	defer stop()

	store := newRedisStore(address, "secret")
	if _, hit := store.get("absent", time.Now()); hit {
		t.Fatal("a key the server does not hold reported a hit")
	}

	store.put("key", sampleEntry(`{"financial_metrics":[]}`, time.Minute))
	entry, hit := store.get("key", time.Now())
	if !hit {
		t.Fatal("a stored entry did not come back")
	}
	if string(entry.body) != `{"financial_metrics":[]}` {
		t.Fatalf("body came back as %q", entry.body)
	}
}

// TestRedisStore_UnreachableIsAMiss checks a Redis that is not running
// costs a miss rather than an error.
func TestRedisStore_UnreachableIsAMiss(t *testing.T) {
	store := newRedisStore("127.0.0.1:1", "")
	store.put("key", sampleEntry("body", time.Minute)) // must not panic
	if _, hit := store.get("key", time.Now()); hit {
		t.Fatal("an unreachable Redis reported a hit")
	}
}

// TestExpiredEntryIsNotStored checks an entry already past its expiry is
// not written, since a backend would only have to expire it immediately.
func TestExpiredEntryIsNotStored(t *testing.T) {
	if _, ok := secondsUntil(time.Now().Add(-time.Second), time.Now()); ok {
		t.Fatal("an already-expired entry was given a positive TTL")
	}
	if seconds, ok := secondsUntil(time.Now().Add(90*time.Second), time.Now()); !ok || seconds < 89 || seconds > 90 {
		t.Fatalf("TTL came out as %d seconds, want 90", seconds)
	}
}

// TestSharedStoreFor_SelectsBackend pins which URL selects which backend,
// and that an unusable configuration selects none rather than a broken one.
func TestSharedStoreFor_SelectsBackend(t *testing.T) {
	if store := sharedStoreFor("https://db.upstash.io", "token"); store == nil {
		t.Fatal("an Upstash URL with a token selected no backend")
	}
	if store := sharedStoreFor("https://db.upstash.io", ""); store != nil {
		t.Fatal("an Upstash URL with no token selected a backend it cannot authenticate to")
	}
	redis, ok := sharedStoreFor("redis://:pass@localhost", "").(*redisStore)
	if !ok {
		t.Fatal("a redis:// URL did not select the Redis backend")
	}
	if redis.address != "localhost:6379" {
		t.Fatalf("address is %q, want the default port supplied", redis.address)
	}
	if redis.password != "pass" {
		t.Fatalf("password is %q, want it read from the URL", redis.password)
	}
	for _, bad := range []string{"", "  ", "memcached://host"} {
		if store := sharedStoreFor(bad, "token"); store != nil {
			t.Fatalf("%q selected a backend", bad)
		}
	}
}

// TestNewCacheStore_UnconfiguredStaysLocal checks this server behaves
// exactly as it did before when no cache is configured.
func TestNewCacheStore_UnconfiguredStaysLocal(t *testing.T) {
	if _, ok := newCacheStore("", "").(*responseCache); !ok {
		t.Fatal("an unconfigured cache is not the plain in-memory one")
	}
	if _, ok := newCacheStore("redis://localhost:6379", "").(*tieredStore); !ok {
		t.Fatal("a configured cache does not answer from memory first")
	}
}

// TestTieredStore_SharedHitFillsMemory checks a machine pays the network
// for a given response at most once.
func TestTieredStore_SharedHitFillsMemory(t *testing.T) {
	shared := newResponseCache()
	shared.put("key", sampleEntry("shared", time.Minute))
	local := newResponseCache()
	tiered := &tieredStore{local: local, shared: shared}

	if _, hit := local.get("key", time.Now()); hit {
		t.Fatal("memory held the entry before anything read it")
	}
	if _, hit := tiered.get("key", time.Now()); !hit {
		t.Fatal("the shared entry was not found")
	}
	if _, hit := local.get("key", time.Now()); !hit {
		t.Fatal("a shared hit was not written back into memory")
	}
}
