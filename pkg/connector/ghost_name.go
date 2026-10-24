package connector

import (
	"cmp"
	"context"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
)

// Ghosts are shared by every login, but contact stores are per login: a sync whose rendered
// displayname carries no contact name must not replace a name another login set.

// holdBackUnnamed makes ui's name, unless named, apply only to a ghost without a name. The check
// runs in ExtraUpdates, under the ghost's syncLock, so a concurrent named update is not overwritten.
// Afterwards a nil ui.Name means the sync must not replace an existing name.
func holdBackUnnamed(ui *bridgev2.UserInfo, named bool) {
	if named || ui.Name == nil {
		return
	}
	fallback := *ui.Name
	ui.Name = nil
	ui.ExtraUpdates = bridgev2.MergeExtraUpdaters(ui.ExtraUpdates, func(ctx context.Context, ghost *bridgev2.Ghost) bool {
		if ghost.Name != "" && ghost.NameSet {
			return false
		}
		// Retries pushing an existing name that failed, else sets the fallback on a nameless ghost.
		return ghost.UpdateName(ctx, cmp.Or(ghost.Name, fallback))
	})
}

// contactNamesGhost reports whether any contact name field changed the rendered displayname,
// without assuming which fields the template reads.
func (c *Config) contactNamesGhost(jid types.JID, phone string, contact types.ContactInfo) bool {
	unnamed := contact
	unnamed.FirstName, unnamed.FullName, unnamed.PushName, unnamed.BusinessName = "", "", "", ""
	return c.FormatDisplayname(jid, phone, contact) != c.FormatDisplayname(jid, phone, unnamed)
}

const contactEventReason = "contact event"

// holdBackContactEventName holds back a contact event's name unless displayname_template reads an
// address book name, the only thing such an event changes; the rest is this login's possibly stale
// view of the contact.
func (c *Config) holdBackContactEventName(reason string, ui *bridgev2.UserInfo) {
	if reason == contactEventReason && !c.readsAddressBook() {
		holdBackUnnamed(ui, false)
	}
}

// readsAddressBook probes with and without push and business names, so a template that reads
// FullName only behind one of them still counts.
func (c *Config) readsAddressBook() bool {
	for _, base := range []types.ContactInfo{{}, {PushName: "push", BusinessName: "business"}} {
		withAddressBook := base
		withAddressBook.FirstName, withAddressBook.FullName = "first", "full"
		if c.FormatDisplayname(types.EmptyJID, "+10", withAddressBook) != c.FormatDisplayname(types.EmptyJID, "+10", base) {
			return true
		}
	}
	return false
}

// altGhostInfo is mainGhost's info for its LID or phone number twin. Unless the sync was named,
// mainGhost's name may be a nameless render.
func altGhostInfo(mainGhost *bridgev2.Ghost, named bool) *bridgev2.UserInfo {
	info := makeInfoFromGhost(mainGhost)
	holdBackUnnamed(info, named)
	return info
}
