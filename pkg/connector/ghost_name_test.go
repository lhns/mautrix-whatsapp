package connector

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
)

const defaultTemplate = `{{or .BusinessName .PushName .Phone .RedactedPhone "Unknown user"}} (WA)`

func TestContactNamesGhost(t *testing.T) {
	phoneJID := types.NewJID("10000000001", types.DefaultUserServer)
	lidJID := types.NewJID("20000000002", types.HiddenUserServer)
	tests := []struct {
		name     string
		template string
		jid      types.JID
		phone    string
		contact  types.ContactInfo
		want     bool
	}{
		{"push name", defaultTemplate, phoneJID, "", types.ContactInfo{PushName: "Push"}, true},
		{"business name", defaultTemplate, phoneJID, "", types.ContactInfo{BusinessName: "Biz"}, true},
		{"address book name the default template does not read", defaultTemplate, phoneJID, "",
			types.ContactInfo{FullName: "Full", FirstName: "First"}, false},
		{"address book name a custom template reads", `{{or .PushName .FullName .Phone}} (WA)`, phoneJID, "",
			types.ContactInfo{FullName: "Full"}, true},
		{"legacy alias for FullName", `{{or .Name .Phone}}`, phoneJID, "", types.ContactInfo{FullName: "Full"}, true},
		{"legacy alias for FirstName", `{{or .Short .Phone}}`, phoneJID, "", types.ContactInfo{FirstName: "First"}, true},
		{"nothing known, phone JID", defaultTemplate, phoneJID, "", types.ContactInfo{}, false},
		{"nothing known, LID without a phone", defaultTemplate, lidJID, "", types.ContactInfo{}, false},
		{"redacted phone is not a name", defaultTemplate, lidJID, "", types.ContactInfo{RedactedPhone: "+1∙∙∙01"}, false},
		{"template that never reads a name", `{{.Phone}} (WA)`, phoneJID, "", types.ContactInfo{PushName: "Push"}, false},
		{"constant template", `WhatsApp user`, phoneJID, "", types.ContactInfo{PushName: "Push"}, false},
		{"push name identical to the phone fallback", defaultTemplate, phoneJID, "",
			types.ContactInfo{PushName: "+10000000001"}, false},
		{"explicit phone for a LID", defaultTemplate, lidJID, "+10000000001", types.ContactInfo{PushName: "Push"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{DisplaynameTemplate: tc.template}
			if err := cfg.PostProcess(); err != nil {
				t.Fatalf("PostProcess() failed: %v", err)
			}
			if got := cfg.contactNamesGhost(tc.jid, tc.phone, tc.contact); got != tc.want {
				t.Errorf("contactNamesGhost() = %v, want %v (rendered %q)",
					got, tc.want, cfg.FormatDisplayname(tc.jid, tc.phone, tc.contact))
			}
		})
	}
}

// nameRecorder is the part of MatrixAPI that Ghost.UpdateName uses.
type nameRecorder struct {
	bridgev2.MatrixAPI
	fail   bool
	pushes int
}

func (r *nameRecorder) SetDisplayName(context.Context, string) error {
	r.pushes++
	if r.fail {
		return errors.New("homeserver unavailable")
	}
	return nil
}

func newTestGhost(name string, nameSet bool) *bridgev2.Ghost {
	return &bridgev2.Ghost{Ghost: &database.Ghost{Name: name, NameSet: nameSet}, Intent: &nameRecorder{}}
}

// applyName does the name part of Ghost.UpdateInfo, including holding the ghost's lock.
func applyName(lock *sync.Mutex, ghost *bridgev2.Ghost, info *bridgev2.UserInfo) {
	lock.Lock()
	defer lock.Unlock()
	ctx := context.Background()
	if info.Name != nil {
		ghost.UpdateName(ctx, *info.Name)
	}
	if info.ExtraUpdates != nil {
		info.ExtraUpdates(ctx, ghost)
	}
}

func renderedInfo(name string, named bool) *bridgev2.UserInfo {
	ui := &bridgev2.UserInfo{Name: ptr.Ptr(name), Identifiers: []string{"tel:+10000000001"}}
	holdBackUnnamed(ui, named, "+10000000001 (WA)")
	return ui
}

func TestHoldBackUnnamed(t *testing.T) {
	const fallback = "+10000000001 (WA)"
	const realName = "Push (WA)"
	tests := []struct {
		name        string
		named       bool
		rendered    string
		ghostName   string
		ghostSet    bool
		failPush    bool
		wantName    string
		wantNameSet bool
	}{
		{"a named render replaces an existing name", true, realName, fallback, true, false, realName, true},
		{"an unnamed render keeps an existing name", false, fallback, realName, true, false, realName, true},
		{"an unnamed render names a new ghost", false, fallback, "", false, false, fallback, true},
		{"an unnamed render retries a name whose push failed", false, fallback, realName, false, false, realName, true},
		{"a failed retry stays unset", false, fallback, realName, false, true, realName, false},
		{"a real render replaces a phone-number-only name", false, realName, fallback, true, false, realName, true},
		{"a phone-number-only render never replaces a real name", false, fallback, realName, true, false, realName, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ghost := newTestGhost(tc.ghostName, tc.ghostSet)
			ghost.Intent.(*nameRecorder).fail = tc.failPush
			info := renderedInfo(tc.rendered, tc.named)
			if len(info.Identifiers) != 1 {
				t.Errorf("holding back the name dropped the identifiers")
			}
			applyName(&sync.Mutex{}, ghost, info)
			if ghost.Name != tc.wantName || ghost.NameSet != tc.wantNameSet {
				t.Errorf("ghost name = %q (set %v), want %q (set %v)", ghost.Name, ghost.NameSet, tc.wantName, tc.wantNameSet)
			}
		})
	}
}

// Two logins sync a new ghost at once: whichever takes the lock first, the named one wins.
func TestHoldBackUnnamedConcurrentLogins(t *testing.T) {
	const realName = "Push (WA)"
	for i := 0; i < 200; i++ {
		ghost := newTestGhost("", false)
		var lock sync.Mutex
		var wg sync.WaitGroup
		for _, info := range []*bridgev2.UserInfo{renderedInfo("+10000000001 (WA)", false), renderedInfo(realName, true)} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				applyName(&lock, ghost, info)
			}()
		}
		wg.Wait()
		if ghost.Name != realName {
			t.Fatalf("run %d: ghost name = %q, want %q", i, ghost.Name, realName)
		}
	}
}

func TestAltGhostInfo(t *testing.T) {
	const fallback = "+10000000001 (WA)"
	const realName = "Push (WA)"
	tests := []struct {
		name        string
		mainName    string
		named       bool
		twinName    string
		twinSet     bool
		wantName    string
		wantNameSet bool
		wantPushes  int
	}{
		{"a nameless sync keeps the twin's name", fallback, false, realName, true, realName, true, 0},
		{"a named sync renames the twin", "Other (WA)", true, realName, true, "Other (WA)", true, 1},
		{"a nameless sync names a new twin", fallback, false, "", false, fallback, true, 1},
		{"a nameless main ghost never blanks the twin", "", false, realName, true, realName, true, 0},
		{"a nameless sync retries the twin's failed push", fallback, false, realName, false, realName, true, 1},
		{"a real name replaces a twin showing only the phone number", realName, false, fallback, true, realName, true, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			main := newTestGhost(tc.mainName, true)
			main.AvatarID = "avatar"
			twin := newTestGhost(tc.twinName, tc.twinSet)
			info := altGhostInfo(makeInfoFromGhost(main), tc.named, fallback)
			if info.Avatar == nil || info.Avatar.ID != "avatar" {
				t.Errorf("the avatar is no longer synced: %+v", info.Avatar)
			}
			applyName(&sync.Mutex{}, twin, info)
			if twin.Name != tc.wantName || twin.NameSet != tc.wantNameSet {
				t.Errorf("twin name = %q (set %v), want %q (set %v)", twin.Name, twin.NameSet, tc.wantName, tc.wantNameSet)
			}
			if pushes := twin.Intent.(*nameRecorder).pushes; pushes != tc.wantPushes {
				t.Errorf("displayname pushes = %d, want %d", pushes, tc.wantPushes)
			}
		})
	}
}

const (
	staleName = "Stale (WA)"
	oldName   = "Old (WA)"
	newName   = "New (WA)"
)

var (
	testPhoneJID = types.NewJID("10000000001", types.DefaultUserServer)
	testLIDJID   = types.NewJID("20000000002", types.HiddenUserServer)
)

func nameTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{DisplaynameTemplate: defaultTemplate}
	if err := cfg.PostProcess(); err != nil {
		t.Fatalf("PostProcess() failed: %v", err)
	}
	return cfg
}

// A render that is not a name event, as contactToUserInfo makes it, never renames a named ghost
// but still carries the avatar.
func TestNonEventRenderKeepsName(t *testing.T) {
	for _, reason := range []string{"picture event", "resync", "history sync", "contact event", "login"} {
		t.Run(reason, func(t *testing.T) {
			ghost := newTestGhost(oldName, true)
			info := renderedInfo(staleName, false)
			info.Avatar = &bridgev2.Avatar{ID: "avatar"}
			applyName(&sync.Mutex{}, ghost, info)
			if ghost.Name != oldName {
				t.Errorf("ghost name = %q, want %q", ghost.Name, oldName)
			}
			if info.Avatar == nil || info.Avatar.ID != "avatar" {
				t.Errorf("avatar dropped: %+v", info.Avatar)
			}
		})
	}
}

func TestResyncNamesUnnamedGhost(t *testing.T) {
	ghost := newTestGhost("", false)
	applyName(&sync.Mutex{}, ghost, renderedInfo(newName, false))
	if ghost.Name != newName || !ghost.NameSet {
		t.Errorf("ghost name = %q (set %v), want %q (set)", ghost.Name, ghost.NameSet, newName)
	}
}

func TestNameEventRenames(t *testing.T) {
	cfg := nameTestConfig(t)
	tests := []struct {
		name     string
		ev       nameEvent
		resolved types.ContactInfo
	}{
		{"push name", pushNameEvent(&events.PushName{JID: testLIDJID, NewPushName: "New"}),
			types.ContactInfo{PushName: "Stale"}},
		{"business name", businessNameEvent(&events.BusinessName{JID: testLIDJID, NewBusinessName: "New"}),
			types.ContactInfo{BusinessName: "Stale"}},
		// resolveContact can overwrite the event's name with the alternate identity's stale one.
		{"push name over a conflicting alternate", pushNameEvent(&events.PushName{JID: testLIDJID, NewPushName: "New"}),
			types.ContactInfo{PushName: "Stale", FullName: "Stale"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ghost := newTestGhost(oldName, true)
			info := renderedInfo(staleName, false)
			cfg.applyNameEvent(info, testLIDJID, "+10000000001", tc.resolved, tc.ev)
			applyName(&sync.Mutex{}, ghost, info)
			if ghost.Name != newName {
				t.Errorf("ghost name = %q, want %q", ghost.Name, newName)
			}
		})
	}
}

func TestEmptyNameEventKeepsName(t *testing.T) {
	cfg := nameTestConfig(t)
	ghost := newTestGhost(oldName, true)
	info := renderedInfo("+10000000001 (WA)", false)
	cfg.applyNameEvent(info, testPhoneJID, "+10000000001", types.ContactInfo{PushName: "Stale"},
		pushNameEvent(&events.PushName{JID: testPhoneJID}))
	applyName(&sync.Mutex{}, ghost, info)
	if ghost.Name != oldName {
		t.Errorf("ghost name = %q, want %q", ghost.Name, oldName)
	}
}

// An event on one form of a contact must reach the ghost of the other form.
func TestNameEventJIDs(t *testing.T) {
	tests := []struct {
		name string
		ev   nameEvent
		want []types.JID
	}{
		{"push name on the phone JID", pushNameEvent(&events.PushName{JID: testPhoneJID, JIDAlt: testLIDJID}),
			[]types.JID{testPhoneJID, testLIDJID}},
		{"push name on the LID", pushNameEvent(&events.PushName{JID: testLIDJID, JIDAlt: testPhoneJID}),
			[]types.JID{testLIDJID, testPhoneJID}},
		{"push name without an alternate", pushNameEvent(&events.PushName{JID: testLIDJID}),
			[]types.JID{testLIDJID}},
		{"business name", businessNameEvent(&events.BusinessName{JID: testPhoneJID}),
			[]types.JID{testPhoneJID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.ev.jids, tc.want) {
				t.Errorf("jids = %v, want %v", tc.ev.jids, tc.want)
			}
		})
	}
}

// The info handed to the twin must not follow later changes to the main ghost.
func TestMakeInfoFromGhostIsSnapshot(t *testing.T) {
	ghost := newTestGhost(oldName, true)
	ghost.ExtraProfile = database.ExtraProfile{"k": json.RawMessage(`"v"`)}
	info := makeInfoFromGhost(ghost)
	ghost.Name = newName
	ghost.ExtraProfile["k"] = json.RawMessage(`"changed"`)
	if *info.Name != oldName || string(info.ExtraProfile["k"]) != `"v"` {
		t.Errorf("snapshot changed with the ghost: %q %v", *info.Name, info.ExtraProfile)
	}
}

// A LID migration copy must not replace the LID ghost's real name by a nameless render.
func TestAltGhostInfoFromMigrationKeepsName(t *testing.T) {
	cfg := nameTestConfig(t)
	nameless := cfg.namelessName(testLIDJID, "+10000000001", types.ContactInfo{})
	pn := newTestGhost("+10000000001 (WA)", true)
	lid := newTestGhost(oldName, true)
	applyName(&sync.Mutex{}, lid, altGhostInfo(makeInfoFromGhost(pn), false, nameless))
	if lid.Name != oldName {
		t.Errorf("LID ghost name = %q, want %q", lid.Name, oldName)
	}
}
