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

// Ghosts are shared by every login, but contact stores are per login, so a render from one login
// may carry a stale or missing name.

// holdBackUnnamed makes ui's name, unless named, apply only to a ghost without a real name: one
// that is empty or still shows nameless, the render without any contact name. It runs in
// ExtraUpdates, under the ghost's syncLock, so a concurrent named update is not overwritten.
// Afterwards a nil ui.Name means the sync must not replace a real name.
func holdBackUnnamed(ui *bridgev2.UserInfo, named bool, nameless string) {
	if named || ui.Name == nil {
		return
	}
	fallback := *ui.Name
	ui.Name = nil
	ui.ExtraUpdates = bridgev2.MergeExtraUpdaters(ui.ExtraUpdates, func(ctx context.Context, ghost *bridgev2.Ghost) bool {
		if ghost.Name != "" && ghost.NameSet && ghost.Name != nameless {
			return false
		}
		// A phone-number-only name counts as none. Else retries pushing a name that failed.
		current := ghost.Name
		if current == nameless {
			current = ""
		}
		return ghost.UpdateName(ctx, cmp.Or(current, fallback))
	})
}

// namelessName renders contact without any contact name field, without assuming which fields the
// template reads.
func (c *Config) namelessName(jid types.JID, phone string, contact types.ContactInfo) string {
	contact.FirstName, contact.FullName, contact.PushName, contact.BusinessName = "", "", "", ""
	return c.FormatDisplayname(jid, phone, contact)
}

// contactNamesGhost reports whether any contact name field changed the rendered displayname.
func (c *Config) contactNamesGhost(jid types.JID, phone string, contact types.ContactInfo) bool {
	return c.FormatDisplayname(jid, phone, contact) != c.namelessName(jid, phone, contact)
}

// nameEvent is a push or business name change, the only thing that renames a named ghost.
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

// applyNameEvent and resolveContact render this login's FullName and FirstName too, so
// displayname_template must not read address book fields: they would rename from a stale contact.
//
// A PushName event renders with the stored BusinessName, which wins under the default template.
//
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

// altGhostInfo is the main ghost's snapshot as info for its LID or phone number twin; unless named,
// the twin keeps its real name, as the main ghost's may be a nameless render. nameless is the
// twin's own nameless render.
func altGhostInfo(main *bridgev2.UserInfo, named bool, nameless string) *bridgev2.UserInfo {
	info := *main
	holdBackUnnamed(&info, named, nameless)
	return &info
}

// updateGhostInfo updates ghost and returns a snapshot of it taken under its lock, so a
// concurrent sync cannot change what is copied to the twin.
func updateGhostInfo(ctx context.Context, ghost *bridgev2.Ghost, info *bridgev2.UserInfo) *bridgev2.UserInfo {
	var snapshot *bridgev2.UserInfo
	info.ExtraUpdates = bridgev2.MergeExtraUpdaters(info.ExtraUpdates, func(_ context.Context, g *bridgev2.Ghost) bool {
		snapshot = makeInfoFromGhost(g)
		return false
	})
	ghost.UpdateInfo(ctx, info)
	return snapshot
}
