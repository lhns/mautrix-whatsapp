package connector

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func testNicknameConfig(t *testing.T, memberNickname string) *Config {
	t.Helper()
	c := &Config{DisplaynameTemplate: testDisplaynameTemplate, MemberNicknameTemplate: memberNickname}
	if err := c.PostProcess(); err != nil {
		t.Fatalf("PostProcess: %v", err)
	}
	return c
}

func TestShouldSetMemberNickname(t *testing.T) {
	if ShouldSetMemberNickname("") {
		t.Error("an empty template should leave member events alone")
	}
	if !ShouldSetMemberNickname(`{{.FullName}}`) {
		t.Error("a template should enable member nicknames")
	}
}

func TestFormatMemberNickname(t *testing.T) {
	tests := []struct {
		name     string
		template string
		contact  types.ContactInfo
		want     string
	}{
		{
			name:     "the contact name is preferred",
			template: `{{or .FullName .BusinessName .PushName .Phone}} (WA)`,
			contact:  types.ContactInfo{FullName: "Contact Name", PushName: "Push Name"},
			want:     "Contact Name (WA)",
		},
		{
			name:     "falls back to the push name",
			template: `{{or .FullName .BusinessName .PushName .Phone}} (WA)`,
			contact:  types.ContactInfo{PushName: "Push Name"},
			want:     "Push Name (WA)",
		},
		{
			name:     "falls back to the phone number",
			template: `{{or .FullName .BusinessName .PushName .Phone}} (WA)`,
			want:     "+491234567890 (WA)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testNicknameConfig(t, tc.template)
			got, err := c.formatMemberNickname(testJID, "", tc.contact)
			if err != nil {
				t.Fatalf("formatMemberNickname: %v", err)
			}
			if got != tc.want {
				t.Errorf("formatMemberNickname() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The nickname must not leak into the shared ghost profile: that is the whole reason the
// displayname template stays contact-independent.
func TestMemberNicknameDoesNotAffectDisplayname(t *testing.T) {
	c := testNicknameConfig(t, `{{.FullName}} (WA)`)
	contact := types.ContactInfo{FullName: "Contact Name", PushName: "Push Name"}

	displayname, err := c.formatDisplayname(testJID, "", contact)
	if err != nil {
		t.Fatalf("formatDisplayname: %v", err)
	}
	if displayname != "Push Name (WA)" {
		t.Errorf("displayname = %q, want the contact-independent push name", displayname)
	}
}

func TestPostProcessRejectsInvalidMemberNicknameTemplate(t *testing.T) {
	c := &Config{DisplaynameTemplate: testDisplaynameTemplate, MemberNicknameTemplate: `{{.FullName`}
	if err := c.PostProcess(); err == nil {
		t.Error("PostProcess should reject an unparseable member nickname template")
	}
}
