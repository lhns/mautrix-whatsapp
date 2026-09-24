package connector

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/types"
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
	fail bool
}

func (r *nameRecorder) SetDisplayName(context.Context, string) error {
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
	holdBackUnnamed(ui, named)
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
