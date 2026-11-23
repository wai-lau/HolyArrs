package metadata

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/outbound"
)

// countingTMDB answers every request with status (200 gives a movie).
func countingTMDB(t *testing.T, status int, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"id":5,"title":"Standalone"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const savedUserKey = "own-secret-key-value"

// fallbackTMDB is the saved-key setup: the own key goes straight to TMDB
// first (own), the HolyArrs metadata server is the backup (server). have
// says whether the backup exists.
func fallbackTMDB(own, server *httptest.Server, have bool) *TMDB {
	return &TMDB{
		Gap: time.Millisecond,
		Route: func(context.Context) TMDBRoute {
			return TMDBRoute{Base: own.URL + "/3", Access: func(context.Context) (string, error) { return savedUserKey, nil }, Direct: true}
		},
		FirstAttempt: 100 * time.Millisecond,
		Fallback: func(context.Context, TMDBRoute) (TMDBRoute, bool) {
			return TMDBRoute{Base: server.URL + "/3", Access: func(context.Context) (string, error) { return fakeCred, nil }}, have
		},
	}
}

func TestTMDBOwnKeyFailsOverToServer(t *testing.T) {
	for name, status := range map[string]int{"5xx": 500, "unavailable": 503, "rate limited": 429, "key refused": 401, "key forbidden": 403} {
		t.Run(name, func(t *testing.T) {
			outbound.Reset()
			t.Cleanup(outbound.Reset)
			var oh, sh atomic.Int32
			p := fallbackTMDB(countingTMDB(t, status, &oh), countingTMDB(t, 200, &sh), true)
			m, err := p.Movie(context.Background(), "tmdb:5")
			if err != nil || m.Name != "Standalone" {
				t.Fatalf("movie %+v %v", m, err)
			}
			if oh.Load() < 1 || sh.Load() != 1 {
				t.Fatalf("own %d server %d", oh.Load(), sh.Load())
			}
		})
	}
}

func TestTMDBFailsOverWhenTMDBIsUnreachableOrSlow(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var sh atomic.Int32
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	p := fallbackTMDB(dead, countingTMDB(t, 200, &sh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || sh.Load() != 1 {
		t.Fatalf("unreachable: %v server=%d", err, sh.Load())
	}

	outbound.Reset()
	sh.Store(0)
	slow := slowTMDB(t, 300*time.Millisecond)
	p = fallbackTMDB(slow, countingTMDB(t, 200, &sh), true)
	p.FirstAttempt = 50 * time.Millisecond
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || sh.Load() != 1 {
		t.Fatalf("slow: %v server=%d", err, sh.Load())
	}
}

func TestTMDBServerUnusedWhenOwnKeyAnswers(t *testing.T) {
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 200, &oh), countingTMDB(t, 200, &sh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil {
		t.Fatal(err)
	}
	if sh.Load() != 0 {
		t.Fatalf("server used %d times", sh.Load())
	}
	// A plain not-found is an answer, not an outage.
	var o2, s2 atomic.Int32
	p = fallbackTMDB(countingTMDB(t, 404, &o2), countingTMDB(t, 200, &s2), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrNotFound) || s2.Load() != 0 {
		t.Fatalf("404: %v server=%d", err, s2.Load())
	}
}

func TestTMDBNoBackupKeepsTheOwnKeyError(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &oh), countingTMDB(t, 200, &sh), false)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrServiceUnavailable) || sh.Load() != 0 {
		t.Fatalf("err %v server=%d", err, sh.Load())
	}
}

func TestTMDBOwnKeyErrorWinsWhenServerFailsToo(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 401, &oh), countingTMDB(t, 500, &sh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrNoAccess) || sh.Load() != 1 {
		t.Fatalf("err %v server=%d", err, sh.Load())
	}
}

// The user's TMDB key never reaches the HolyArrs server, in a header or in
// the address.
func TestTMDBOwnKeyIsNotSentToServer(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var leaked atomic.Bool
	var oh atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), savedUserKey) || strings.Contains(r.Header.Get("Authorization"), savedUserKey) {
			leaked.Store(true)
		}
		_, _ = w.Write([]byte(`{"id":5,"title":"Standalone"}`))
	}))
	t.Cleanup(server.Close)
	p := fallbackTMDB(countingTMDB(t, 503, &oh), server, true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || leaked.Load() {
		t.Fatalf("err %v leaked %v", err, leaked.Load())
	}
}

// The fallback applies to searches too.
func TestTMDBSearchFailsOverToServer(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &oh), countingTMDB(t, 200, &sh), true)
	_, _ = p.Search(context.Background(), "dune", 0)
	if sh.Load() != 1 {
		t.Fatalf("search server=%d", sh.Load())
	}
}

// Hosted: own key first only in the server mode with a saved key. Without a
// key the server answers alone; Your own keys and Off keep their meaning.
func TestHostedOwnKeyFirstOnlyInServerModeWithSavedKey(t *testing.T) {
	t.Setenv(EnvServiceURL, "")
	t.Setenv(EnvTMDBKey, "")
	t.Setenv(EnvTokenFile, "")
	t.Setenv("HOLYARRS_TMDB_URL", "")
	t.Setenv(EnvMode, "")
	ctx := context.Background()
	v := testVault(t)
	h, err := NewHosted(v)
	if err != nil {
		t.Fatal(err)
	}
	if r := h.TMDBRoute(ctx); r.Direct || r.Off {
		t.Fatalf("server route expected without a key: %+v", r)
	}
	if _, ok := h.TMDBFallback(ctx, h.TMDBRoute(ctx)); ok {
		t.Fatal("fallback without a saved key")
	}
	if err := v.SetSetting(ctx, KeyGroup, KeyTMDB, "saved-own-key", database.System); err != nil {
		t.Fatal(err)
	}
	if r := h.TMDBRoute(ctx); !r.Direct || r.Off {
		t.Fatalf("server mode with key goes to TMDB first: %+v", r)
	}
	if r, ok := h.TMDBFallback(ctx, h.TMDBRoute(ctx)); !ok || r.Direct || r.Off {
		t.Fatalf("server is the backup: %+v %v", r, ok)
	}
	// Your own keys: TMDB only, never the server.
	if err := h.Modes.Set(ModeOwn); err != nil {
		t.Fatal(err)
	}
	if r := h.TMDBRoute(ctx); !r.Direct {
		t.Fatalf("own mode: %+v", r)
	}
	if _, ok := h.TMDBFallback(ctx, h.TMDBRoute(ctx)); ok {
		t.Fatal("own mode must not fall back to the server")
	}
	if err := h.Modes.Set(ModeOff); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.TMDBFallback(ctx, h.TMDBRoute(ctx)); ok {
		t.Fatal("fallback in off mode")
	}
	if r := h.TMDBRoute(ctx); !r.Off {
		t.Fatalf("off mode: %+v", r)
	}
}

// A hung TMDB costs the first attempt once, then the route rests: later
// requests go straight to the server.
func TestTMDBOwnKeyRestsAfterAFailure(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var sh atomic.Int32
	var oh atomic.Int32
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oh.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(hung.Close)
	p := fallbackTMDB(hung, countingTMDB(t, 200, &sh), true)
	p.FirstAttempt = 80 * time.Millisecond
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || sh.Load() != 1 || oh.Load() != 1 {
		t.Fatalf("first: %v server=%d own=%d", err, sh.Load(), oh.Load())
	}
	start := time.Now()
	if _, err := p.Movie(context.Background(), "tmdb:6"); err != nil || sh.Load() != 2 || oh.Load() != 1 {
		t.Fatalf("second: %v server=%d own=%d", err, sh.Load(), oh.Load())
	}
	if time.Since(start) > 60*time.Millisecond {
		t.Fatalf("second request waited on the resting route: %v", time.Since(start))
	}
	// After the rest the own key is tried again.
	p.CoolOff = time.Millisecond
	p.mu.Lock()
	p.coolUntil = map[string]time.Time{}
	p.mu.Unlock()
	if _, err := p.Movie(context.Background(), "tmdb:7"); err != nil || oh.Load() != 2 {
		t.Fatalf("third: %v own=%d", err, oh.Load())
	}
}

// A refused key rests too, and says so once.
func TestTMDBRefusedKeyRestsAndLogsOnce(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 403, &oh), countingTMDB(t, 200, &sh), true)
	p.CoolOff = time.Millisecond
	for i := 5; i < 8; i++ {
		if _, err := p.Movie(context.Background(), "tmdb:"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if oh.Load() != 3 || sh.Load() != 3 {
		t.Fatalf("own %d server %d", oh.Load(), sh.Load())
	}
	if n := strings.Count(buf.String(), "refused the saved TMDB key"); n != 1 {
		t.Fatalf("logged %d times: %q", n, buf.String())
	}
}

// A missing credential is a setup error, not a revoked key: no fallback.
func TestTMDBNoCredentialDoesNotFallBack(t *testing.T) {
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 200, &oh), countingTMDB(t, 200, &sh), true)
	base := p.Route
	p.Route = func(ctx context.Context) TMDBRoute { r := base(ctx); r.Access = nil; return r }
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrNoAccess) || sh.Load() != 0 {
		t.Fatalf("err %v server=%d", err, sh.Load())
	}
}

// After an own-key 429 the fetch-ahead lane still reaches the server: each
// route has its own hold.
func TestTMDBAheadFallsBackAfterOwnKeyRateLimit(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 429, &oh), countingTMDB(t, 200, &sh), true)
	p.AheadGap = time.Millisecond
	m, err := p.Movie(Ahead(context.Background()), "tmdb:5")
	if err != nil || m.Name != "Standalone" || sh.Load() != 1 {
		t.Fatalf("ahead: %+v %v server=%d", m, err, sh.Load())
	}
}

// The server's own refusal of this install is not swallowed on fallback.
func TestTMDBFallbackKeepsInstallRefused(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &oh), countingTMDB(t, 401, &sh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrInstallRefused) {
		t.Fatalf("err %v", err)
	}
}

// A 404 from the server after an own-key 5xx is an answer.
func TestTMDBServerNotFoundAfterOwnFailure(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 500, &oh), countingTMDB(t, 404, &sh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestTMDBConcurrentFetchesFailOverOnce(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh, sh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &oh), countingTMDB(t, 200, &sh), true)
	var wg sync.WaitGroup
	for i := 5; i < 9; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Movie(context.Background(), "tmdb:"+strconv.Itoa(i)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if sh.Load() != 4 {
		t.Fatalf("server %d", sh.Load())
	}
}

// Batch prefetch: off while the saved key answers (the server is only the
// backup), on through the server while the key rests, kept where get looks.
func TestPrefetchWithOwnKeyFirst(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	srv, batches, _ := batchStandIn(t, true)
	var oh atomic.Int32
	own := countingTMDB(t, 200, &oh)
	p := &TMDB{
		Gap: 1,
		Route: func(context.Context) TMDBRoute {
			return TMDBRoute{Base: own.URL + "/3", Access: func(context.Context) (string, error) { return savedUserKey, nil }, Direct: true}
		},
		Fallback: func(context.Context, TMDBRoute) (TMDBRoute, bool) {
			return TMDBRoute{Base: srv.URL + TMDBPath, Access: func(context.Context) (string, error) { return "tok", nil }, HTTP: http.DefaultClient}, true
		},
	}
	ctx := context.Background()
	reads := []TMDBRead{MovieSearchRead("Alien", 0)}
	if n := p.Prefetch(ctx, reads); n != 0 || batches.Load() != 0 {
		t.Fatalf("own key answering: kept %d batches %d", n, batches.Load())
	}
	p.cool(own.URL+"/3", ErrServiceUnavailable)
	if n := p.Prefetch(ctx, reads); n != 1 || batches.Load() != 1 {
		t.Fatalf("own key resting: kept %d batches %d", n, batches.Load())
	}
	if ms, err := p.Search(ctx, "Alien", 0); err != nil || len(ms) != 1 || ms[0].Name != "From Batch" || oh.Load() != 0 {
		t.Fatalf("search: %v %+v own=%d", err, ms, oh.Load())
	}
}
