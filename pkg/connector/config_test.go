package connector

import (
	"testing"

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

// The precedence rule: private_chat_portal_meta decides whether a DM room gets metadata at
// all, and the template only decides the name when it does.
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
			if got := ShouldSetDMRoomName(tc.dmRoomNameTemplate, tc.privateChatPortalMeta); got != tc.want {
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
		// Empty must stay empty: wrapDMInfo leaves ChatInfo.Name nil on "", which is what
		// keeps bridgev2's ghost-based naming in place.
		{"empty when nothing matches", `{{.FullName}}`, types.ContactInfo{}, ""},
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

// A contact name set on the room must not appear on the ghost, which every login shares.
// Also catches both templates being backed by the same parsed template.
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

// A contact's DM portal can be keyed under either identity, so naming its rooms has to look
// up both.
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

// A DM portal is always keyed to the login that owns it. DM room naming depends on this:
// it looks portals up by receiver, so one login cannot find another login's DM with the
// same contact.
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
		// Every server a DM can live on. Group portals are deliberately left shared unless
		// split_portals is on, so they are not part of this invariant.
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
