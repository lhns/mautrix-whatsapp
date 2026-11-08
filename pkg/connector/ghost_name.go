package connector

import (
	"cmp"
	"context"
	"slices"

	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
)

// Ghosts are shared by every login, but contact stores are per login: a sync whose rendered
// displayname carries no contact name must not replace a name another login set.

// holdBackUnnamed stops a displayname that no contact name contributed to from replacing the
// ghost's name. The check runs in ExtraUpdates, under the ghost's syncLock, so a concurrent named
// update is not overwritten. Afterwards ui.Name is nil exactly when the sync carried no contact name.
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

// nameEvent is a push or business name change, the only thing that renames a named ghost. Ghosts
// are shared by every login, contact stores are not, so any other render may carry a stale name.
type nameEvent struct {
	jids []types.JID
	set  func(*types.ContactInfo)
}

// pushNameEvent covers both the phone and LID form of the contact, as the ghost may use either.
func pushNameEvent(evt *events.PushName) nameEvent {
	return nameEvent{
		jids: nonEmptyJIDs(evt.JID, evt.JIDAlt),
		set:  func(c *types.ContactInfo) { c.PushName = evt.NewPushName },
	}
}

func businessNameEvent(evt *events.BusinessName) nameEvent {
	return nameEvent{
		jids: nonEmptyJIDs(evt.JID),
		set:  func(c *types.ContactInfo) { c.BusinessName = evt.NewBusinessName },
	}
}

func nonEmptyJIDs(jids ...types.JID) []types.JID {
	var out []types.JID
	for _, jid := range jids {
		if !jid.IsEmpty() && !slices.Contains(out, jid) {
			out = append(out, jid)
		}
	}
	return out
}

// applyNameEvent renames from the event's name rather than the contact store's, which resolveContact
// may have overridden with the alternate identity's (possibly stale) name.
func (wa *WhatsAppClient) applyNameEvent(ctx context.Context, jid types.JID, ui *bridgev2.UserInfo, ev nameEvent) error {
	contact, err := wa.GetStore().Contacts.GetContact(ctx, jid)
	if err != nil {
		return err
	}
	resolved, phone := wa.resolveContact(ctx, jid, contact)
	wa.Main.Config.applyNameEvent(ui, jid, phone, resolved, ev)
	return nil
}

func (c *Config) applyNameEvent(ui *bridgev2.UserInfo, jid types.JID, phone string, resolved types.ContactInfo, ev nameEvent) {
	ev.set(&resolved)
	if c.contactNamesGhost(jid, phone, resolved) {
		ui.Name = ptr.Ptr(c.FormatDisplayname(jid, phone, resolved))
	}
}

// altGhostInfo is mainGhost's info for its LID or phone number twin. Unless the sync was named,
// mainGhost's name may be a nameless render.
func altGhostInfo(mainGhost *bridgev2.Ghost, named bool) *bridgev2.UserInfo {
	info := makeInfoFromGhost(mainGhost)
	holdBackUnnamed(info, named)
	return info
}
