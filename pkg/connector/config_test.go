package connector

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const testDisplaynameTemplate = `{{or .BusinessName .PushName .Phone "Unknown user"}} (WA)`

var testJID = types.NewJID("491234567890", types.DefaultUserServer)

func testConfig(t *testing.T, dmRoomName string) *Config {
	t.Helper()
	c := &Config{DisplaynameTemplate: testDisplaynameTemplate, DMRoomNameTemplate: dmRoomName}
	if err := c.PostProcess(); err != nil {
		t.Fatalf("PostProcess: %v", err)
	}
	return c
}

func TestShouldSetDMRoomName(t *testing.T) {
	tests := []struct {
		name                  string
		dmRoomNameTemplate    string
		privateChatPortalMeta bool
		want                  bool
	}{
		{"template names the room", `{{.FullName}}`, true, true},
		{"no template falls back to the ghost", "", true, false},
		{"switch off means no room metadata at all", `{{.FullName}}`, false, false},
		{"neither", "", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{DMRoomNameTemplate: tc.dmRoomNameTemplate}
			if got := c.shouldSetDMRoomName(tc.privateChatPortalMeta); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatDMRoomName(t *testing.T) {
	const tpl = `{{or .FullName .BusinessName .PushName .Phone}}`
	tests := []struct {
		name     string
		template string
		contact  types.ContactInfo
		want     string
	}{
		{"contact name wins", tpl, types.ContactInfo{FullName: "Mum", PushName: "not this"}, "Mum"},
		{"then business name", tpl, types.ContactInfo{BusinessName: "Bakery", PushName: "not this"}, "Bakery"},
		{"then push name", tpl, types.ContactInfo{PushName: "pushy"}, "pushy"},
		{"then phone", tpl, types.ContactInfo{}, "+491234567890"},
		// A render nothing about the contact reached falls back to the displayname, so a DM room
		// is never left without a custom name.
		{"displayname when nothing matches", `{{.FullName}}`, types.ContactInfo{PushName: "pushy"}, "pushy (WA)"},
		{"displayname when only the suffix is left", `{{.FullName}} (WA)`, types.ContactInfo{}, "+491234567890 (WA)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testConfig(t, tc.template).formatDMRoomName(testJID, "", tc.contact)
			if err != nil {
				t.Fatalf("formatDMRoomName: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A contact name set on the room must not appear on the ghost, which every login shares. Also
// catches both templates ending up backed by the same parsed template.
func TestDMRoomNameDoesNotAffectDisplayname(t *testing.T) {
	contact := types.ContactInfo{FullName: "Mum", PushName: "pushy"}
	want := testConfig(t, "").FormatDisplayname(testJID, "", contact)
	got := testConfig(t, `{{.FullName}}`).FormatDisplayname(testJID, "", contact)
	if got != want {
		t.Errorf("displayname changed when dm_room_name_template was set: got %q, want %q", got, want)
	}
	if got == "Mum" {
		t.Errorf("contact name leaked into the shared ghost displayname: %q", got)
	}
}

// An invalid template must fail at config load rather than at runtime.
func TestPostProcessRejectsInvalidDMRoomNameTemplate(t *testing.T) {
	c := &Config{DisplaynameTemplate: testDisplaynameTemplate, DMRoomNameTemplate: "{{.FullName"}
	if err := c.PostProcess(); err == nil {
		t.Error("PostProcess accepted an unparseable dm_room_name_template")
	}
}

// A contact's DM portal can be keyed under either identity, so naming its rooms looks up both.
func TestMakeUserIDDiffersBetweenPhoneAndLID(t *testing.T) {
	phone := waid.MakeUserID(types.NewJID("491234567890", types.DefaultUserServer))
	lid := waid.MakeUserID(types.NewJID("491234567890", types.HiddenUserServer))
	if phone == "" || lid == "" {
		t.Fatalf("expected both keyings to produce an ID, got %q and %q", phone, lid)
	}
	if phone == lid {
		t.Errorf("phone and LID JIDs collapsed to the same user ID (%q), so one lookup would suffice", phone)
	}
}

// DM room naming looks portals up by receiver, so it depends on a DM portal always being keyed
// to the login that owns it: one login must not find another login's DM with the same contact.
func TestDMPortalKeyIsAlwaysScopedToTheLogin(t *testing.T) {
	const me = networkid.UserLoginID("491111111111")
	for _, splitPortals := range []bool{false, true} {
		wa := &WhatsAppClient{
			UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: me}},
			Main: &WhatsAppConnector{
				Bridge: &bridgev2.Bridge{
					Config: &bridgeconfig.BridgeConfig{SplitPortals: splitPortals},
				},
			},
		}
		// Every server a DM can live on. Group portals stay shared unless split_portals is on,
		// so they are not part of this invariant.
		for _, server := range []string{
			types.DefaultUserServer,
			types.HiddenUserServer,
			types.BotServer,
			types.BroadcastServer,
		} {
			key := wa.makeWAPortalKey(types.NewJID("491234567890", server))
			if key.Receiver != me {
				t.Errorf("split_portals=%v, server=%s: receiver = %q, want the login ID",
					splitPortals, server, key.Receiver)
			}
		}
	}
}

// With no template, DM rooms are sent the default-name marker, which clears the custom-name flag
// an earlier template left so the room follows the ghost's name and avatar again.
func TestWrapDMInfoWithoutTemplate(t *testing.T) {
	const me = "491111111111"
	for _, tc := range []struct {
		name     string
		meta     bool
		jid      types.JID
		wantName *string
	}{
		{"private_chat_portal_meta off leaves the name alone", false, testJID, nil},
		{"no template resets to the ghost's name", true, testJID, bridgev2.DefaultChatName},
		{"the chat with yourself is left alone", true, types.NewJID(me, types.DefaultUserServer), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wa := &WhatsAppClient{
				JID:       types.NewJID(me, types.DefaultUserServer),
				UserLogin: &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: me}},
				Main: &WhatsAppConnector{
					Config: *testConfig(t, ""),
					Bridge: &bridgev2.Bridge{Config: &bridgeconfig.BridgeConfig{
						PrivateChatPortalMeta: tc.meta,
						SplitPortals:          true,
					}},
				},
			}
			info := wa.wrapDMInfo(context.Background(), tc.jid)
			if info.Name != tc.wantName {
				t.Errorf("Name = %v, want %v", info.Name, tc.wantName)
			}
			if info.Avatar != nil {
				t.Errorf("Avatar = %+v, want nil without a template", info.Avatar)
			}
		})
	}
}

// fakeContactStore serves contacts and LID mappings from maps; everything else is a no-op.
type fakeContactStore struct {
	*store.NoopStore
	contacts map[types.JID]types.ContactInfo
	lidToPN  map[types.JID]types.JID
}

func (f *fakeContactStore) GetContact(_ context.Context, jid types.JID) (types.ContactInfo, error) {
	return f.contacts[jid], nil
}

func (f *fakeContactStore) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	return f.lidToPN[lid], nil
}

func (f *fakeContactStore) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	for lid, p := range f.lidToPN {
		if p == pn {
			return lid, nil
		}
	}
	return types.EmptyJID, nil
}

// With a template set, every DM gets a non-empty name, so bridgev2 never falls back to copying
// the shared ghost's name into the room.
func TestDMRoomNameAlwaysNames(t *testing.T) {
	const tpl = `{{or .FullName .BusinessName .PushName .Phone}} (WA)`
	lid := types.NewJID("20000000002", types.HiddenUserServer)
	mappedLID := types.NewJID("30000000003", types.HiddenUserServer)
	fake := &fakeContactStore{
		NoopStore: &store.NoopStore{},
		contacts: map[types.JID]types.ContactInfo{
			testJID: {FullName: "Mum"},
		},
		lidToPN: map[types.JID]types.JID{mappedLID: testJID},
	}
	failing := &store.NoopStore{Error: errors.New("store unavailable")}
	for _, tc := range []struct {
		name  string
		store store.AllStores
		jid   types.JID
		want  string
	}{
		{"contact name", fake, testJID, "Mum (WA)"},
		{"LID resolves the phone contact", fake, mappedLID, "Mum (WA)"},
		{"LID-only contact falls back to the displayname", fake, lid, "Unknown user (WA)"},
		{"unreadable contact store still names the room", failing, testJID, "+491234567890 (WA)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wa := &WhatsAppClient{
				Client: &whatsmeow.Client{Store: &store.Device{Contacts: tc.store, LIDs: tc.store}},
				Main:   &WhatsAppConnector{Config: *testConfig(t, tpl)},
			}
			if got := wa.dmRoomName(context.Background(), tc.jid); got != tc.want {
				t.Errorf("dmRoomName = %q, want %q", got, tc.want)
			}
		})
	}
}
