package connector

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestFillMissingContactFields(t *testing.T) {
	tests := []struct {
		name    string
		contact types.ContactInfo
		other   types.ContactInfo
		want    types.ContactInfo
	}{
		{
			name:    "an unknown push name is filled from the other login",
			contact: types.ContactInfo{FullName: "Contact Name"},
			other:   types.ContactInfo{PushName: "Push Name"},
			want:    types.ContactInfo{FullName: "Contact Name", PushName: "Push Name"},
		},
		{
			name:    "an unknown business name is filled from the other login",
			contact: types.ContactInfo{PushName: "Push Name"},
			other:   types.ContactInfo{BusinessName: "Business Name"},
			want:    types.ContactInfo{PushName: "Push Name", BusinessName: "Business Name"},
		},
		{
			// What this login already holds is what it most recently heard from WhatsApp, so
			// it wins. Nothing here picks between two values that are both present.
			name:    "a known push name is kept, not replaced",
			contact: types.ContactInfo{PushName: "Mine"},
			other:   types.ContactInfo{PushName: "Theirs"},
			want:    types.ContactInfo{PushName: "Mine"},
		},
		{
			name:    "a known business name is kept, not replaced",
			contact: types.ContactInfo{BusinessName: "Mine"},
			other:   types.ContactInfo{BusinessName: "Theirs"},
			want:    types.ContactInfo{BusinessName: "Mine"},
		},
		{
			// The case that made both logins render different names and flip-flop.
			name:    "push here, business there: both end up set",
			contact: types.ContactInfo{PushName: "Push Name"},
			other:   types.ContactInfo{BusinessName: "Business Name"},
			want:    types.ContactInfo{PushName: "Push Name", BusinessName: "Business Name"},
		},
		{
			name:    "the other login knows nothing either",
			contact: types.ContactInfo{FullName: "Contact Name"},
			other:   types.ContactInfo{},
			want:    types.ContactInfo{FullName: "Contact Name"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fillMissingContactFields(tc.contact, tc.other)
			if got.PushName != tc.want.PushName || got.BusinessName != tc.want.BusinessName ||
				got.FullName != tc.want.FullName {
				t.Errorf("fillMissingContactFields() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

const defaultTemplate = `{{or .BusinessName .PushName .Phone .RedactedPhone "Unknown user"}} (WA)`

// contactNamesGhost must not assume which fields a template reads, so every case is run against
// the template that reads them and one that doesn't.
func TestContactNamesGhost(t *testing.T) {
	phoneJID := types.NewJID("491512345678", types.DefaultUserServer)
	lidJID := types.NewJID("123456789", types.HiddenUserServer)
	tests := []struct {
		name     string
		template string
		jid      types.JID
		phone    string
		contact  types.ContactInfo
		want     bool
	}{
		{
			name:     "push name",
			template: defaultTemplate,
			jid:      phoneJID,
			contact:  types.ContactInfo{PushName: "Push Name"},
			want:     true,
		},
		{
			name:     "business name",
			template: defaultTemplate,
			jid:      phoneJID,
			contact:  types.ContactInfo{BusinessName: "Business Name"},
			want:     true,
		},
		{
			// The default template does not read FullName, so this renders as the phone number.
			name:     "address book name the default template does not read",
			template: defaultTemplate,
			jid:      phoneJID,
			contact:  types.ContactInfo{FullName: "Contact Name", FirstName: "Contact"},
			want:     false,
		},
		{
			// The same contact under a template that does read it: the answer must flip.
			name:     "address book name a custom template does read",
			template: `{{or .BusinessName .PushName .FullName .Phone "Unknown user"}} (WA)`,
			jid:      phoneJID,
			contact:  types.ContactInfo{FullName: "Contact Name", FirstName: "Contact"},
			want:     true,
		},
		{
			name:     "legacy alias for FullName",
			template: `{{or .VName .Notify .Name .Phone "Unknown user"}} (WA)`,
			jid:      phoneJID,
			contact:  types.ContactInfo{FullName: "Contact Name"},
			want:     true,
		},
		{
			name:     "legacy alias for FirstName",
			template: `{{or .Short .Phone "Unknown user"}} (WA)`,
			jid:      phoneJID,
			contact:  types.ContactInfo{FirstName: "Contact"},
			want:     true,
		},
		{
			name:     "nothing known, phone JID falls back to the number",
			template: defaultTemplate,
			jid:      phoneJID,
			contact:  types.ContactInfo{},
			want:     false,
		},
		{
			// A LID with no phone number anywhere renders as the template's last resort.
			name:     "nothing known, LID without a phone",
			template: defaultTemplate,
			jid:      lidJID,
			contact:  types.ContactInfo{},
			want:     false,
		},
		{
			// RedactedPhone is a ContactInfo field but is not a name: clearing it would make
			// this look like a name that the template used.
			name:     "redacted phone is not a name",
			template: defaultTemplate,
			jid:      lidJID,
			contact:  types.ContactInfo{RedactedPhone: "+4∙∙∙∙∙∙∙78"},
			want:     false,
		},
		{
			name:     "template that never reads a name",
			template: `{{.Phone}} (WA)`,
			jid:      phoneJID,
			contact:  types.ContactInfo{PushName: "Push Name", BusinessName: "Business Name"},
			want:     false,
		},
		{
			name:     "constant template",
			template: `WhatsApp user`,
			jid:      phoneJID,
			contact:  types.ContactInfo{PushName: "Push Name"},
			want:     false,
		},
		{
			// A push name that happens to equal the fallback carries no more information than
			// the fallback, so treating it as unnamed is correct.
			name:     "push name identical to the phone fallback",
			template: defaultTemplate,
			jid:      phoneJID,
			contact:  types.ContactInfo{PushName: "+491512345678"},
			want:     false,
		},
		{
			name:     "explicit phone argument is used for a LID",
			template: defaultTemplate,
			jid:      lidJID,
			phone:    "+491512345678",
			contact:  types.ContactInfo{PushName: "Push Name"},
			want:     true,
		},
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
