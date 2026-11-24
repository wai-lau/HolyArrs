package accounts

import (
	"context"
	"strings"
	"testing"
)

func permsOf(t *testing.T, f *fixture, id int64) string {
	t.Helper()
	var perms string
	if err := f.s.Read().QueryRow("SELECT perms FROM users WHERE id = ?", id).Scan(&perms); err != nil {
		t.Fatal(err)
	}
	return perms
}

// TestJoinAutoApprove: with the setting on, new people from an outside
// sign-in start with "ask" plus "auto_approve" and nothing more; Kids stay
// ask-only; people who already exist keep what they have; off changes nothing.
func TestJoinAutoApprove(t *testing.T) {
	f := gated(t, Options{})
	own := ownerOf(t, f)
	ctx := context.Background()
	join := func(ext string, kid bool) int64 {
		t.Helper()
		id, _, err := f.a.linkedUser(ctx, ProviderJellyfin, ext, ext, true, kid, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	set := func(on string) {
		t.Helper()
		if resp, out := own.do(t, f, "PUT", "/api/v1/auth/settings", `{"join_auto_approve":`+on+`}`); resp.StatusCode != 200 || !strings.Contains(out, `"join_auto_approve":`+on) {
			t.Fatalf("settings: %d %s", resp.StatusCode, out)
		}
	}

	before := join("before", false)
	if got := permsOf(t, f, before); got != `["ask"]` {
		t.Fatalf("default join perms %s", got)
	}

	set("true")
	after := join("after", false)
	if got := permsOf(t, f, after); got != `["ask","auto_approve"]` {
		t.Fatalf("join with setting on: %s", got)
	}
	if got := permsOf(t, f, before); got != `["ask"]` {
		t.Fatalf("setting changed an existing account: %s", got)
	}
	if got := permsOf(t, f, join("child", true)); got != `["ask"]` {
		t.Fatalf("kid joined with %s", got)
	}
	id, _, err := f.a.ssoIdentity(ctx, "oidc", "https://id.example.test", "sub-1", "sso@example.test", true, 0)
	if err != nil || permsOf(t, f, id) != `["ask","auto_approve"]` {
		t.Fatalf("single sign-on join: %v %s", err, permsOf(t, f, id))
	}

	set("false")
	if got := permsOf(t, f, join("off", false)); got != `["ask"]` {
		t.Fatalf("join with setting off: %s", got)
	}
}
