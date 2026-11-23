package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func fallbackTMDB(server, own *httptest.Server, have bool) *TMDB {
	cred := func(context.Context) (string, error) { return fakeCred, nil }
	return &TMDB{
		Gap: time.Millisecond,
		Route: func(context.Context) TMDBRoute {
			return TMDBRoute{Base: server.URL + "/3", Access: cred}
		},
		Fallback: func(context.Context) (TMDBRoute, bool) {
			return TMDBRoute{Base: own.URL + "/3", Access: cred, Direct: true}, have
		},
	}
}

func TestTMDBFallsBackToOwnKeyWhenServerFails(t *testing.T) {
	for name, status := range map[string]int{"5xx": 500, "unavailable": 503} {
		t.Run(name, func(t *testing.T) {
			outbound.Reset()
			t.Cleanup(outbound.Reset)
			var sh, oh atomic.Int32
			p := fallbackTMDB(countingTMDB(t, status, &sh), countingTMDB(t, 200, &oh), true)
			m, err := p.Movie(context.Background(), "tmdb:5")
			if err != nil || m.Name != "Standalone" {
				t.Fatalf("movie %+v %v", m, err)
			}
			if sh.Load() != 1 || oh.Load() != 1 {
				t.Fatalf("server %d own %d", sh.Load(), oh.Load())
			}
		})
	}
}

func TestTMDBFallsBackWhenServerIsUnreachableOrSlow(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var oh atomic.Int32
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	p := fallbackTMDB(dead, countingTMDB(t, 200, &oh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || oh.Load() != 1 {
		t.Fatalf("unreachable: %v own=%d", err, oh.Load())
	}

	outbound.Reset()
	oh.Store(0)
	slow := slowTMDB(t, 300*time.Millisecond)
	p = fallbackTMDB(slow, countingTMDB(t, 200, &oh), true)
	p.Timeout = 50 * time.Millisecond
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil || oh.Load() != 1 {
		t.Fatalf("slow: %v own=%d", err, oh.Load())
	}
}

func TestTMDBOwnKeyUnusedWhenServerAnswers(t *testing.T) {
	var sh, oh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 200, &sh), countingTMDB(t, 200, &oh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); err != nil {
		t.Fatal(err)
	}
	if oh.Load() != 0 {
		t.Fatalf("own key used %d times", oh.Load())
	}
	// A plain not-found is an answer, not an outage.
	var s2, o2 atomic.Int32
	p = fallbackTMDB(countingTMDB(t, 404, &s2), countingTMDB(t, 200, &o2), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrNotFound) || o2.Load() != 0 {
		t.Fatalf("404: %v own=%d", err, o2.Load())
	}
}

func TestTMDBNoOwnKeyKeepsTheServerError(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var sh, oh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &sh), countingTMDB(t, 200, &oh), false)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrServiceUnavailable) || oh.Load() != 0 {
		t.Fatalf("err %v own=%d", err, oh.Load())
	}
}

func TestTMDBServerErrorWinsWhenOwnKeyFailsToo(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var sh, oh atomic.Int32
	p := fallbackTMDB(countingTMDB(t, 503, &sh), countingTMDB(t, 500, &oh), true)
	if _, err := p.Movie(context.Background(), "tmdb:5"); !errors.Is(err, ErrServiceUnavailable) || oh.Load() != 1 {
		t.Fatalf("err %v own=%d", err, oh.Load())
	}
}

// The fallback applies to searches too, and a Direct primary route never
// falls back to itself.
func TestTMDBSearchFallsBackAndDirectRouteDoesNot(t *testing.T) {
	outbound.Reset()
	t.Cleanup(outbound.Reset)
	var sh, oh atomic.Int32
	own := countingTMDB(t, 200, &oh)
	p := fallbackTMDB(countingTMDB(t, 503, &sh), own, true)
	_, _ = p.Search(context.Background(), "dune", 0)
	if oh.Load() != 1 {
		t.Fatalf("search own=%d", oh.Load())
	}
	outbound.Reset()
	var dh, oh2 atomic.Int32
	direct := countingTMDB(t, 503, &dh)
	p = fallbackTMDB(direct, countingTMDB(t, 200, &oh2), true)
	p.Route = func(context.Context) TMDBRoute {
		return TMDBRoute{Base: direct.URL + "/3", Access: func(context.Context) (string, error) { return fakeCred, nil }, Direct: true}
	}
	if _, err := p.Movie(context.Background(), "tmdb:5"); err == nil || oh2.Load() != 0 {
		t.Fatalf("direct route: %v own=%d", err, oh2.Load())
	}
}

// Hosted: the fallback exists only in the server mode with a saved own key.
func TestHostedFallbackOnlyInServerModeWithSavedKey(t *testing.T) {
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
	if _, ok := h.TMDBFallback(ctx); ok {
		t.Fatal("fallback without a saved key")
	}
	if err := v.SetSetting(ctx, KeyGroup, KeyTMDB, "saved-own-key", database.System); err != nil {
		t.Fatal(err)
	}
	if r, ok := h.TMDBFallback(ctx); !ok || !r.Direct {
		t.Fatalf("server mode with key: %+v %v", r, ok)
	}
	for _, m := range []Mode{ModeOwn, ModeOff} {
		if err := h.Modes.Set(m); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.TMDBFallback(ctx); ok {
			t.Fatalf("fallback in mode %s", m)
		}
	}
}
