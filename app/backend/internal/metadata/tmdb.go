package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/outbound"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/startup"
)

// ErrNoAccess means TMDB or the metadata server refused the credential, or none is set.
var ErrNoAccess = errors.New("the metadata server refused this install; searches and new artwork are unavailable")

// ImageBase is where TMDB serves pictures.
const ImageBase = "https://image.tmdb.org/t/p"

// Attribution is the notice TMDB asks apps that use its API to show.
const Attribution = "This product uses the TMDB API but is not endorsed or certified by TMDB."

// EnvTokenFile names a file holding the TMDB read access token (for the lab).
const EnvTokenFile = "HOLYARRS_TMDB_TOKEN_FILE"

// TMDB looks movies up on The Movie Database. It takes either credential
// TMDB issues: a v4 read token (Bearer header) or a v3 API key (?api_key=).
//
// Answers are kept in memory for TTL. With Disk set, every answer but a
// search is kept there too for tmdbKeep, so a restart reads it back: an
// answer older than TTL is served at once and asked again in the
// background, and a 404 is remembered for tmdbMissTTL. A failed or held
// request serves the old copy when there is one.
//
// A fetch made ahead of a person (Ahead) carries X-Fetch: ahead, is spaced
// at least AheadGap apart, is not sent while the server asked us to wait,
// and has its own breaker, so warm-ups never hold back a person's page.
type TMDB struct {
	BaseURL string                                    // default https://api.themoviedb.org/3
	Access  func(ctx context.Context) (string, error) // the read access token, read on each request; never logged
	HTTP    *http.Client
	Timeout time.Duration // per request, default tmdbAttempt
	Gap     time.Duration // least time between requests, default 50ms
	// AheadGap is the least time between fetch-ahead requests, default
	// 250ms (the server's per-install budget leaves room for pages).
	AheadGap time.Duration
	TTL      time.Duration // cache time, default 6h
	// Images is where pictures are served (ImageBase when empty).
	Images string
	// Disk keeps answers across restarts (lookup_cache); nil keeps memory only.
	Disk Cache
	// Route, when set, picks where each request goes (the metadata mode)
	// in place of BaseURL, Access and HTTP.
	Route func(ctx context.Context) TMDBRoute
	// Fallback, when set and ok, is a second route a request tries once when
	// the first could not answer it (down, slow, 5xx, rate limit, or a
	// refused own key). It is given the first route, so it need not read the
	// vault again. While it is ok the first attempt is also cut to
	// FirstAttempt, and a first route that failed is skipped for ownCoolOff.
	Fallback func(ctx context.Context, first TMDBRoute) (TMDBRoute, bool)
	// FirstAttempt is how long the first route gets when a Fallback exists;
	// default firstAttemptWithFallback.
	FirstAttempt time.Duration
	// CoolOff is how long a first route that failed is skipped; default ownCoolOff.
	CoolOff time.Duration

	mu         sync.Mutex
	last       time.Time
	lastAhead  time.Time
	cache      map[string]cached
	refreshing map[string]bool
	hold       serviceHold // the metadata server's Retry-After
	directHold serviceHold // TMDB's own, for a direct route: apart, so one route's 429 never blocks the other
	// coolUntil: first routes (by address) skipped until then; keyWarned: a
	// refused key already logged, until that route answers again.
	coolUntil map[string]time.Time
	keyWarned map[string]bool
	// batchOffUntil: the server lacked the batch route; reads go one by one until then.
	batchOffUntil time.Time
	// texts are titles' texts per app language, by "kind|id" (title_text.go).
	texts map[string]map[string]TitleText
}

type cached struct {
	body []byte
	at   time.Time
	miss bool // a 404
}

const maxCache = 1000

const (
	// tmdbKeep is how long an answer on disk may be served stale.
	tmdbKeep = 7 * 24 * time.Hour
	// tmdbMissTTL is how long a 404 is remembered.
	tmdbMissTTL = time.Hour
	// tmdbRefreshWait bounds one background refresh.
	tmdbRefreshWait = 30 * time.Second
)

// tmdbDisk is one answer as kept in lookup_cache.
type tmdbDisk struct {
	At   time.Time       `json:"at"`
	Miss bool            `json:"miss,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

// TokenFile reads the token from a file on each call, so a changed file is
// picked up without a restart.
func TokenFile(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading the TMDB token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
}

func (t *TMDB) images() string {
	if t.Images == "" {
		return ImageBase
	}
	return strings.TrimRight(t.Images, "/")
}

func (t *TMDB) base() string {
	if t.BaseURL == "" {
		return "https://api.themoviedb.org/3"
	}
	return strings.TrimRight(t.BaseURL, "/")
}

// wait spaces requests by Gap, and fetch-ahead requests by AheadGap too.
func (t *TMDB) wait(ctx context.Context) error {
	gap := t.Gap
	if gap <= 0 {
		gap = 50 * time.Millisecond
	}
	t.mu.Lock()
	next := t.last.Add(gap)
	if now := time.Now(); next.Before(now) {
		next = now
	}
	if IsAhead(ctx) {
		ag := t.AheadGap
		if ag <= 0 {
			ag = 250 * time.Millisecond
		}
		if a := t.lastAhead.Add(max(ag, gap)); a.After(next) {
			next = a
		}
		t.lastAhead = next
	}
	t.last = next
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(next)):
		return nil
	}
}

// kept reads u's answer from memory, else from Disk when disk is set.
func (t *TMDB) kept(ctx context.Context, u string, disk bool) (cached, bool) {
	t.mu.Lock()
	c, ok := t.cache[u]
	t.mu.Unlock()
	if ok || !disk {
		return c, ok
	}
	b, ok, err := t.Disk.Get(ctx, "tmdb:"+u, time.Now())
	if err != nil || !ok {
		return cached{}, false
	}
	var d tmdbDisk
	if json.Unmarshal(b, &d) != nil || d.At.IsZero() || !d.Miss && len(d.Body) == 0 {
		return cached{}, false
	}
	c = cached{body: d.Body, at: d.At, miss: d.Miss}
	t.remember(u, c)
	return c, true
}

func (t *TMDB) remember(u string, c cached) {
	t.mu.Lock()
	// Low-memory mode keeps fewer answers here: the Disk copy answers the rest.
	if t.cache == nil || len(t.cache) >= startup.LowMemoryPick(maxCache, 150) {
		t.cache = map[string]cached{}
	}
	t.cache[u] = c
	t.mu.Unlock()
}

// keep stores a fetched answer (or a 404) in memory and on Disk.
func (t *TMDB) keep(ctx context.Context, u string, c cached, disk bool) {
	t.remember(u, c)
	if !disk {
		return
	}
	b, err := json.Marshal(tmdbDisk{At: c.at, Miss: c.miss, Body: c.body})
	if err != nil {
		return
	}
	until := c.at.Add(tmdbKeep)
	if c.miss {
		until = c.at.Add(tmdbMissTTL)
	}
	_ = t.Disk.Put(context.WithoutCancel(ctx), "tmdb:"+u, b, c.at, until)
}

// refresh asks for u again in the background (once at a time), as a
// fetch-ahead, and keeps the answer.
func (t *TMDB) refresh(u string, disk bool) {
	t.mu.Lock()
	if t.refreshing[u] {
		t.mu.Unlock()
		return
	}
	if t.refreshing == nil {
		t.refreshing = map[string]bool{}
	}
	t.refreshing[u] = true
	t.mu.Unlock()
	go func() {
		defer func() {
			t.mu.Lock()
			delete(t.refreshing, u)
			t.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(Ahead(context.Background()), tmdbRefreshWait)
		defer cancel()
		body, err := t.fetch(ctx, u)
		switch {
		case err == nil:
			t.keep(ctx, u, cached{body: body, at: time.Now()}, disk)
		case errors.Is(err, ErrNotFound):
			t.keep(ctx, u, cached{at: time.Now(), miss: true}, disk)
		}
	}()
}

// tmdbStaleOK says whether err may be answered from an old copy: the
// service failed, was held or is rate limited (not a refusal or a 404).
func tmdbStaleOK(ctx context.Context, err error) bool {
	return err != nil && ctx.Err() == nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNoAccess)
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	r := t.route(ctx)
	u := r.base() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	ttl := t.TTL
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	// Searches hold the person's words: they stay in memory only.
	disk := t.Disk != nil && !strings.HasPrefix(path, "/search")
	if r.Off {
		return t.keptOnly(ctx, u, disk)
	}
	var stale *cached
	if !noCache(ctx) {
		if c, ok := t.kept(ctx, u, disk); ok {
			switch {
			case c.miss && time.Since(c.at) < tmdbMissTTL:
				return nil, ErrNotFound
			case c.miss:
			case time.Since(c.at) < ttl:
				return c.body, nil
			default:
				stale = &c
			}
		}
	}
	if stale != nil && disk {
		t.refresh(u, disk)
		return stale.body, nil
	}
	body, err := t.fetchOn(ctx, u, r)
	switch {
	case err == nil:
		t.keep(ctx, u, cached{body: body, at: time.Now()}, disk)
	case errors.Is(err, ErrNotFound):
		t.keep(ctx, u, cached{at: time.Now(), miss: true}, disk)
	case stale != nil && tmdbStaleOK(ctx, err):
		return stale.body, nil
	}
	return body, err
}

// errBadQuery is a 400: the metadata server refused the query itself.
var errBadQuery = errors.New("query refused")

// tmdbAttempt is how long one request may take once the Gate lets it go. An
// answer the server has not kept yet can take 20 s or more: it waits its
// turn upstream, then keeps the answer for the next ask.
const tmdbAttempt = 25 * time.Second

// firstAttemptWithFallback is how long the first route gets when a second
// could answer instead: long enough for a normal answer, short enough that
// the backup still helps while TMDB hangs.
const firstAttemptWithFallback = 5 * time.Second

// ownCoolOff is how long the first route is skipped after it failed to
// answer (slow, down, 5xx, rate limit, or a refused key). A timeout never
// opens the breaker, so without this every request would wait again.
const ownCoolOff = 60 * time.Second

// keyRefused is a 401 or 403 from TMDB for the key sent: the key is revoked
// or wrong. It is an ErrNoAccess too.
type keyRefused struct{}

func (keyRefused) Error() string { return ErrNoAccess.Error() }
func (keyRefused) Unwrap() error { return ErrNoAccess }

// shouldFallBack says whether err from route r is TMDB (or the server) not
// answering, as opposed to answering "no". Down, slow, 5xx and the rate limit
// fall back. So does a refused own key (401 or 403): a revoked key must not
// take every lookup down while the server can answer. A not-found is an
// answer and never falls back; a refusal by the metadata server itself, or a
// missing credential (a setup error), is left alone.
func shouldFallBack(err error, r TMDBRoute) bool {
	switch {
	case errors.Is(err, ErrServiceUnavailable), errors.Is(err, ErrRateLimited):
		return true
	case r.Direct && errors.As(err, new(keyRefused)):
		return true
	}
	return false
}

func (t *TMDB) coolOff() time.Duration {
	if t.CoolOff > 0 {
		return t.CoolOff
	}
	return ownCoolOff
}

func (t *TMDB) cooling(base string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return time.Now().Before(t.coolUntil[base])
}

// cool skips the route at base for CoolOff; a refused key is logged once.
func (t *TMDB) cool(base string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.coolUntil == nil {
		t.coolUntil = map[string]time.Time{}
	}
	t.coolUntil[base] = time.Now().Add(t.coolOff())
	if errors.As(err, new(keyRefused)) && !t.keyWarned[base] {
		if t.keyWarned == nil {
			t.keyWarned = map[string]bool{}
		}
		t.keyWarned[base] = true
		log.Printf("warn: metadata: TMDB refused the saved TMDB key; using the HolyArrs server until it works again. Check the key in Settings > Metadata")
	}
}

func (t *TMDB) answered(base string) {
	t.mu.Lock()
	delete(t.keyWarned, base)
	t.mu.Unlock()
}

// fetch makes one request (one retry after a 429) on the route now.
func (t *TMDB) fetch(ctx context.Context, u string) ([]byte, error) {
	return t.fetchOn(ctx, u, t.route(ctx))
}

// fetchOn is fetch with the route already read (one vault read per request).
// When the first route cannot answer and a Fallback route exists (the owner's
// saved TMDB key went first, the metadata server is next), the request is
// sent once more there, and the first route rests for CoolOff.
func (t *TMDB) fetchOn(ctx context.Context, u string, r TMDBRoute) ([]byte, error) {
	var fb TMDBRoute
	hasFB := false
	if t.Fallback != nil && !r.Off {
		fb, hasFB = t.Fallback(ctx, r)
	}
	rest, ok := strings.CutPrefix(u, r.base())
	if hasFB && !ok {
		hasFB = false
	}
	if !hasFB {
		return t.fetchVia(ctx, u, r, 0)
	}
	if t.cooling(r.base()) {
		return t.fetchVia(ctx, fb.base()+rest, fb, 0)
	}
	attempt := t.FirstAttempt
	if attempt <= 0 {
		attempt = firstAttemptWithFallback
	}
	body, err := t.fetchVia(ctx, u, r, attempt)
	if err == nil {
		t.answered(r.base())
	}
	if err == nil || ctx.Err() != nil || !shouldFallBack(err, r) {
		return body, err
	}
	t.cool(r.base(), err)
	b2, err2 := t.fetchVia(ctx, fb.base()+rest, fb, 0)
	if err2 != nil && !errors.Is(err2, ErrNotFound) {
		if errors.Is(err2, ErrInstallRefused) {
			return nil, err2 // the server's own sign-in problem: the install must register again
		}
		return nil, err // the first route's error is the one to show
	}
	return b2, err2
}

// fetchVia is fetch on one route; attempt 0 means the default time.
func (t *TMDB) fetchVia(ctx context.Context, u string, r TMDBRoute, attempt time.Duration) ([]byte, error) {
	ahead := IsAhead(ctx)
	hold := &t.hold
	if r.Direct {
		hold = &t.directHold
	}
	if ahead && hold.held(time.Now()) {
		return nil, ErrRateLimited
	}
	if r.Off {
		return nil, ErrSourceOff
	}
	if r.Access == nil {
		return nil, ErrNoAccess
	}
	tok, err := r.Access(ctx)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, ErrNoAccess
	}
	bearer := strings.TrimSpace(tok)
	if r.Direct {
		bearer = tmdbApply(&u, tok)
	}
	// Fetch-ahead and a person's pages fail apart: a warm-up that keeps
	// failing never opens the breaker a page call goes through.
	lane := "page"
	if ahead {
		lane = "ahead"
	}
	op, err := outbound.Begin(ctx, outbound.Target{Kind: "metadata_tmdb", Identity: []string{r.base(), tok, lane}})
	if err != nil {
		if errors.Is(err, outbound.ErrSuppressed) {
			return nil, fmt.Errorf("TMDB: %w (%w)", ErrServiceUnavailable, err)
		}
		return nil, err
	}
	outcome := outbound.Canceled
	defer func() { op.Finish(outcome) }()
	cl := r.HTTP
	if cl == nil {
		cl = http.DefaultClient
	}
	timeout := t.Timeout
	switch {
	case attempt > 0:
		timeout = attempt
	case timeout <= 0:
		timeout = tmdbAttempt
	}
	for try := 0; ; try++ {
		if err := t.wait(ctx); err != nil {
			return nil, err
		}
		rctx, cancel := afterGate(ctx, timeout)
		req, _ := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
		setCredential(req, bearer)
		req.Header.Set("Accept", "application/json")
		aheadRequest(ctx, req)
		resp, err := cl.Do(req)
		if err != nil {
			outcome = outbound.HTTPOutcome(0, err, ctx)
			why := ErrServiceUnavailable
			// The attempt's own clock, or the client's (its Timeout may be
			// shorter than the attempt: 20 s against 25 s): either way the
			// server was slow, not down. A connect timeout is not this: it
			// is not a DeadlineExceeded.
			if timedOut(ctx, rctx) || (ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)) {
				outcome, why = outbound.Canceled, ErrServiceSlow // slow, or our own Gate's wait: not down
			}
			cancel()
			return nil, fmt.Errorf("TMDB: %w (%w)", why, stripURL(err))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
		cancel()
		if err != nil {
			outcome = outbound.HTTPOutcome(resp.StatusCode, err, ctx)
			if timedOut(ctx, rctx) {
				outcome = outbound.Canceled
				return nil, fmt.Errorf("TMDB: %w (%w)", ErrServiceSlow, stripURL(err))
			}
			return nil, fmt.Errorf("TMDB: %w", stripURL(err))
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			outcome = outbound.Success
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			outcome = outbound.TerminalFailure
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized && !r.Direct:
			// The metadata server no longer knows this install's sign-in;
			// it is dropped and the install signs up again on its own.
			outcome = outbound.TerminalFailure
			return nil, InstallRefused(ErrNoAccess)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			outcome = outbound.TerminalFailure
			return nil, keyRefused{}
		case resp.StatusCode == http.StatusTooManyRequests && try == 0 && !ahead:
			d, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(min(max(d, 0), 10)) * time.Second):
			}
		case resp.StatusCode == http.StatusTooManyRequests:
			// Fetch-ahead does not wait inside the call: it holds back instead.
			hold.set(resp.Header.Get("Retry-After"), body, time.Now())
			outcome = outbound.TransientFailure
			if resp.Header.Get(GateHeld) != "" {
				outcome = outbound.Canceled // held by our own Gate, never sent
			}
			return nil, ErrRateLimited
		default:
			outcome = outbound.HTTPOutcome(resp.StatusCode, nil, ctx)
			if resp.StatusCode == http.StatusServiceUnavailable {
				hold.set(resp.Header.Get("Retry-After"), body, time.Now())
			}
			if resp.StatusCode >= 500 {
				return nil, fmt.Errorf("TMDB answered %d: %w", resp.StatusCode, ErrServiceUnavailable)
			}
			if resp.StatusCode == http.StatusBadRequest {
				return nil, fmt.Errorf("TMDB answered %d: %w", resp.StatusCode, errBadQuery)
			}
			return nil, fmt.Errorf("TMDB answered %d", resp.StatusCode)
		}
	}
}

// stripURL drops the URL from a transport error; it holds the search words.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Search finds movies by name, and by year when year is not 0.
func (t *TMDB) Search(ctx context.Context, query string, year int) ([]Movie, error) {
	rd := MovieSearchRead(query, year)
	b, err := t.get(ctx, rd.Path, rd.Query)
	if err != nil {
		return nil, err
	}
	var res struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("TMDB: odd answer: %w", err)
	}
	out := make([]Movie, 0, len(res.Results))
	for _, m := range res.Results {
		out = append(out, m.movie(t.images()))
	}
	return out, nil
}

// Movie takes "tmdb:<id>" or "imdb:<tt id>".
func (t *TMDB) Movie(ctx context.Context, id string) (Movie, error) {
	src, v, ok := strings.Cut(id, ":")
	if !ok || v == "" {
		return Movie{}, ErrNotFound
	}
	switch src {
	case "imdb":
		if !strings.HasPrefix(v, "tt") || strings.ContainsAny(v, "/?#") {
			return Movie{}, ErrNotFound
		}
		b, err := t.get(ctx, "/find/"+url.PathEscape(v), url.Values{"external_source": {"imdb_id"}})
		if err != nil {
			return Movie{}, err
		}
		var res struct {
			Movies []tmdbMovie `json:"movie_results"`
		}
		if err := json.Unmarshal(b, &res); err != nil || len(res.Movies) == 0 {
			return Movie{}, ErrNotFound
		}
		return t.Movie(ctx, "tmdb:"+strconv.FormatInt(res.Movies[0].ID, 10))
	case "tmdb":
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return Movie{}, ErrNotFound
		}
		rd := MovieDetailRead(n)
		b, err := t.get(ctx, rd.Path, rd.Query)
		if err != nil {
			return Movie{}, err
		}
		var m tmdbMovie
		if err := json.Unmarshal(b, &m); err != nil {
			return Movie{}, fmt.Errorf("TMDB: odd answer: %w", err)
		}
		return m.movieWithCollection(t.images(), true), nil
	}
	return Movie{}, ErrNotFound
}

// maxChangePages caps how many pages of the changed list are read.
const maxChangePages = 50

// Changed lists the movies TMDB changed since since (TMDB keeps 14 days).
func (t *TMDB) Changed(ctx context.Context, since time.Time) ([]string, error) {
	var out []string
	start := since.UTC().Format("2006-01-02")
	end := time.Now().UTC().Format("2006-01-02")
	ctx = NoCache(ctx)
	for page := 1; page <= maxChangePages; page++ {
		b, err := t.get(ctx, "/movie/changes", url.Values{"start_date": {start}, "end_date": {end}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		var res struct {
			Results []struct {
				ID int64 `json:"id"`
			} `json:"results"`
			Page       int `json:"page"`
			TotalPages int `json:"total_pages"`
		}
		if err := json.Unmarshal(b, &res); err != nil {
			return nil, fmt.Errorf("TMDB: odd answer: %w", err)
		}
		for _, r := range res.Results {
			out = append(out, "tmdb:"+strconv.FormatInt(r.ID, 10))
		}
		if page >= res.TotalPages {
			break
		}
	}
	return out, nil
}
