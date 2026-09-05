package connector

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

const testGroupNameDisplayname = `{{or .BusinessName .PushName .Phone "Unknown user"}} (WA)`

var testGroupJID = types.NewJID("120363000000000000", types.GroupServer)

func testGroupConfig(t *testing.T, groupRoomName string) *Config {
	t.Helper()
	c := &Config{DisplaynameTemplate: testGroupNameDisplayname, GroupRoomNameTemplate: groupRoomName}
	if err := c.PostProcess(); err != nil {
		t.Fatalf("PostProcess: %v", err)
	}
	return c
}

func TestShouldSetGroupRoomName(t *testing.T) {
	if ShouldSetGroupRoomName("") {
		t.Error("an empty template should leave group names alone")
	}
	if !ShouldSetGroupRoomName(`{{.Name}} (WA)`) {
		t.Error("a template should enable group room naming")
	}
}

func TestFormatGroupRoomName(t *testing.T) {
	tests := []struct {
		name     string
		template string
		subject  string
		want     string
	}{
		{
			// The stock path: no template means the subject is passed through untouched, so
			// existing rooms are not renamed by upgrading.
			name:    "no template uses the subject unchanged",
			subject: "Group Subject",
			want:    "Group Subject",
		},
		{
			name:     "the template renders the subject",
			template: `{{.Name}} (WA)`,
			subject:  "Group Subject",
			want:     "Group Subject (WA)",
		},
		{
			name:     "an empty subject still renders",
			template: `{{or .Name "Unnamed group"}} (WA)`,
			want:     "Unnamed group (WA)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testGroupConfig(t, tc.template)
			got, err := c.formatGroupRoomName(testGroupJID, tc.subject)
			if err != nil {
				t.Fatalf("formatGroupRoomName: %v", err)
			}
			if got != tc.want {
				t.Errorf("formatGroupRoomName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatGroupRoomNameExposesJID(t *testing.T) {
	c := testGroupConfig(t, `{{.JID}}`)
	got, err := c.formatGroupRoomName(testGroupJID, "Group Subject")
	if err != nil {
		t.Fatalf("formatGroupRoomName: %v", err)
	}
	if got != testGroupJID.String() {
		t.Errorf("formatGroupRoomName() = %q, want the JID", got)
	}
}

// The group template must not touch ghost displaynames.
func TestGroupRoomNameDoesNotAffectDisplayname(t *testing.T) {
	c := testGroupConfig(t, `{{.Name}} (WA)`)
	got, err := c.formatDisplayname(types.NewJID("491234567890", types.DefaultUserServer), "", types.ContactInfo{PushName: "Push Name"})
	if err != nil {
		t.Fatalf("formatDisplayname: %v", err)
	}
	if got != "Push Name (WA)" {
		t.Errorf("displayname = %q, want it unaffected by the group template", got)
	}
}

func TestPostProcessRejectsInvalidGroupRoomNameTemplate(t *testing.T) {
	c := &Config{DisplaynameTemplate: testGroupNameDisplayname, GroupRoomNameTemplate: `{{.Name`}
	if err := c.PostProcess(); err == nil {
		t.Error("PostProcess should reject an unparseable group room name template")
	}
}
