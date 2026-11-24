package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/database"
	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// What new people start with: the owner picks the request permissions
// everyone made from then on gets. It covers people who sign in with Plex,
// Jellyfin, Emby, single sign-on or a trusted proxy, people the owner
// imports from a media server, and invite links that do not pick their own.
// Kids stay ask-only and admins keep the admin defaults.
const (
	keyNewPeoplePerms = "new_people_perms" // JSON list; unset means the default
	// keyJoinAutoApprove was an on/off switch for ask plus auto-approve. An
	// install that turned it on keeps that until a list is saved.
	keyJoinAutoApprove = "join_auto_approve"
)

// newPeopleOptions are the permissions the list may hold, in screen order:
// the request permissions. Approving, the Library and settings are powers
// the owner gives person by person, never to everyone who joins.
var newPeopleOptions = []webserver.Perm{webserver.PermAsk, webserver.PermAutoApprove, webserver.PermAutoApproveFourK}

// newPeoplePerms is what a new member starts with: the saved list, the old
// switch's reading when no list was saved, or today's default (ask only).
func (a *Auth) newPeoplePerms(ctx context.Context) []webserver.Perm {
	if raw := a.signInSetting(ctx, keyNewPeoplePerms); raw != "" {
		return onlyOptions(parsePerms(raw))
	}
	if a.signInSetting(ctx, keyJoinAutoApprove) == "true" {
		return []webserver.Perm{webserver.PermAsk, webserver.PermAutoApprove}
	}
	return webserver.DefaultPerms(webserver.RoleMember)
}

// onlyOptions keeps the request permissions of a list, in screen order.
func onlyOptions(in []webserver.Perm) []webserver.Perm {
	out := []webserver.Perm{}
	for _, p := range newPeopleOptions {
		if slices.Contains(in, p) {
			out = append(out, p)
		}
	}
	return out
}

// readNewPeoplePerms checks a list sent to be saved: only request
// permissions, and 4K approval only together with approval.
func readNewPeoplePerms(raw []string) ([]webserver.Perm, error) {
	in, err := readPerms(raw)
	if err != nil {
		return nil, err
	}
	for _, p := range in {
		if !slices.Contains(newPeopleOptions, p) {
			return nil, errors.New("new people can start with asking and auto-approve only; " + string(p) + " is given one person at a time")
		}
	}
	if slices.Contains(in, webserver.PermAutoApproveFourK) && !slices.Contains(in, webserver.PermAutoApprove) {
		return nil, errors.New("4K requests approved without waiting also needs requests approved without waiting")
	}
	return onlyOptions(in), nil
}

// mergeNewPeople gives a member the list as their request permissions and
// keeps every other power they hold.
func mergeNewPeople(have, list []webserver.Perm) []webserver.Perm {
	out := slices.DeleteFunc(slices.Clone(have), func(p webserver.Perm) bool { return slices.Contains(newPeopleOptions, p) })
	return append(out, list...)
}

// membersToChange are the members (Kids, admins and the owner left out) whose
// request permissions differ from the list, with what they would hold.
func membersToChange(ctx context.Context, q conn, list []webserver.Perm) (map[int64][]webserver.Perm, error) {
	rows, err := adultMemberPerms(ctx, q)
	if err != nil {
		return nil, err
	}
	out := map[int64][]webserver.Perm{}
	for id, raw := range rows {
		have := parsePerms(raw)
		if next := mergeNewPeople(have, list); !samePerms(next, have) {
			out[id] = next
		}
	}
	return out, nil
}

// newPeopleView is what the Users screen shows: the list, the boxes it may
// tick and how many members Apply would change.
func (a *Auth) newPeopleView(ctx context.Context) (map[string]any, error) {
	list := a.newPeoplePerms(ctx)
	change, err := membersToChange(ctx, a.s.Read(), list)
	if err != nil {
		return nil, err
	}
	return map[string]any{"perms": list, "options": newPeopleOptions, "to_change": len(change)}, nil
}

// route: PUT /api/v1/users/new-people
func (a *Auth) putNewPeople(w http.ResponseWriter, r *http.Request) {
	if !webserver.RequireStrong(w, r) {
		return
	}
	by, _ := webserver.From(r.Context())
	var body struct {
		Perms *[]string `json:"perms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Perms == nil {
		fail(w, http.StatusBadRequest, `send {"perms": ["ask", "auto_approve"]}`)
		return
	}
	list, err := readNewPeoplePerms(*body.Perms)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, _ := json.Marshal(list)
	err = a.s.WriteCore(r.Context(), func(tx *sql.Tx) error {
		return database.PutSetting(tx, settingsGroup, signInPrefix+keyNewPeoplePerms, string(raw), webserver.Actor(r.Context()), a.now())
	})
	if err != nil {
		serverError(w, "new people", err)
		return
	}
	name, what := newPeopleWords, permWords(list)
	a.accountEvent(r, by.UserID, "new_people_set", byName(r)+" changed "+name+": "+what, map[string]any{"perms": list})
	v, err := a.newPeopleView(r.Context())
	if err != nil {
		serverError(w, "new people", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// route: POST /api/v1/users/new-people/apply
// applyNewPeople sets the saved list on the members already here: not the
// owner, admins or Kids, and not the other powers a member holds.
func (a *Auth) applyNewPeople(w http.ResponseWriter, r *http.Request) {
	if !webserver.RequireStrong(w, r) {
		return
	}
	by, _ := webserver.From(r.Context())
	list := a.newPeoplePerms(r.Context())
	actor, _ := json.Marshal(webserver.Actor(r.Context()))
	changed := 0
	err := a.s.WriteCore(r.Context(), func(tx *sql.Tx) error {
		change, err := membersToChange(r.Context(), tx, list)
		if err != nil {
			return err
		}
		for id, next := range change {
			if err := setMemberPerms(r.Context(), tx, id, permsJSON(next), database.FormatTime(a.now()), string(actor)); err != nil {
				return err
			}
		}
		changed = len(change)
		return nil
	})
	if err != nil {
		serverError(w, "new people", err)
		return
	}
	if changed > 0 {
		name, what := newPeopleWords, permWords(list)+", set on "+strconv.Itoa(changed)+" current members"
		a.accountEvent(r, by.UserID, "new_people_applied", byName(r)+" changed "+name+": "+what,
			map[string]any{"perms": list, "changed": changed})
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "perms": list})
}

// newPeopleWords names the setting in an Event line, which reads
// "<name> changed <setting>: <what>" like the other account changes.
var newPeopleWords = "what new people start with"

// permWords lists permissions for an Event line.
func permWords(list []webserver.Perm) string {
	if len(list) == 0 {
		return "nothing"
	}
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = string(p)
	}
	return strings.Join(out, ", ")
}
