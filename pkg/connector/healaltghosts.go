package connector

// TEMPORARY, branch heal-alt-ghost-names only, never merged: heals LID/phone-number ghost pairs
// that the unguarded alternate-ghost copy left with a real name on one twin and a nameless render
// on the other. Run once with LHNS_HEAL_ALT_GHOST_NAMES=dry-run, then apply, then go back to the
// normal image. Keeps no "done" marker: a rerun only finds what is still half-named.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	healAltGhostNamesEnv = "LHNS_HEAL_ALT_GHOST_NAMES"
	// Lets the logins connect first.
	healAltGhostNamesDelay = time.Minute
	// Synapse fans a displayname change out to every room the ghost is in.
	healAltGhostNamesPause = 2 * time.Second

	healOutcomeHeal         = "heal"
	healOutcomeBothNamed    = "both_named"
	healOutcomeBothNameless = "both_nameless"
	healOutcomeMissingTwin  = "missing_twin"
	healOutcomeRenamed      = "renamed_meanwhile"
)

type altGhostPair struct{ lid, pn types.JID }

func (wa *WhatsAppConnector) healAltGhostNames(ctx context.Context) {
	mode := os.Getenv(healAltGhostNamesEnv)
	if mode == "" || wa.Bridge.Background {
		return
	}
	log := wa.Bridge.Log.With().Str("action", "heal alt ghost names").Str("mode", mode).Logger()
	if mode != "dry-run" && mode != "apply" {
		log.Warn().Msg("Unknown " + healAltGhostNamesEnv + " value, not healing")
		return
	}
	apply := mode == "apply"
	select {
	case <-time.After(healAltGhostNamesDelay):
	case <-ctx.Done():
		return
	}
	pairs, err := wa.findHalfNamedAltGhostCandidates(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to find alt ghost pairs")
		return
	}
	log.Info().Int("candidate_pairs", len(pairs)).Msg("Checking alt ghost pairs with differing names")
	counts := map[string]int{}
	for _, pair := range pairs {
		lidGhost, err := wa.Bridge.GetExistingGhostByID(ctx, waid.MakeUserID(pair.lid))
		if err != nil {
			log.Err(err).Stringer("lid", pair.lid).Msg("Failed to get LID ghost")
			continue
		}
		pnGhost, err := wa.Bridge.GetExistingGhostByID(ctx, waid.MakeUserID(pair.pn))
		if err != nil {
			log.Err(err).Stringer("pn", pair.pn).Msg("Failed to get phone number ghost")
			continue
		}
		outcome := wa.Config.healAltGhostPair(log.WithContext(ctx), lidGhost, pnGhost, pair, apply)
		counts[outcome]++
		if apply && outcome == healOutcomeHeal {
			select {
			case <-time.After(healAltGhostNamesPause):
			case <-ctx.Done():
				return
			}
		}
	}
	verb := "would_heal"
	if apply {
		verb = "healed"
	}
	log.Info().
		Int("candidate_pairs", len(pairs)).
		Int(verb, counts[healOutcomeHeal]).
		Int(healOutcomeRenamed, counts[healOutcomeRenamed]).
		Int(healOutcomeBothNamed, counts[healOutcomeBothNamed]).
		Int(healOutcomeBothNameless, counts[healOutcomeBothNameless]).
		Int(healOutcomeMissingTwin, counts[healOutcomeMissingTwin]).
		Msg("Finished healing alt ghost names")
}

// findHalfNamedAltGhostCandidates lists the LID/phone-number pairs whose ghosts both exist and are
// named differently; healAltGhostPair decides which of them are half-named.
func (wa *WhatsAppConnector) findHalfNamedAltGhostCandidates(ctx context.Context) ([]altGhostPair, error) {
	const query = `
		SELECT m.lid, m.pn
		FROM whatsmeow_lid_map m
		JOIN ghost l ON l.bridge_id=$1 AND l.id='lid-'||m.lid
		JOIN ghost p ON p.bridge_id=$1 AND p.id=m.pn
		WHERE l.name<>p.name
	`
	rows, err := wa.Bridge.DB.Query(ctx, query, wa.Bridge.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs []altGhostPair
	for rows.Next() {
		var lid, pn string
		if err = rows.Scan(&lid, &pn); err != nil {
			return nil, fmt.Errorf("failed to scan pair: %w", err)
		}
		pairs = append(pairs, altGhostPair{
			lid: types.NewJID(lid, types.HiddenUserServer),
			pn:  types.NewJID(pn, types.DefaultUserServer),
		})
	}
	return pairs, rows.Err()
}

// healAltGhostPair logs and, if apply is set, heals one pair. A dry run never touches the ghosts.
func (c *Config) healAltGhostPair(ctx context.Context, lidGhost, pnGhost *bridgev2.Ghost, pair altGhostPair, apply bool) string {
	log := zerolog.Ctx(ctx).With().Stringer("lid", pair.lid).Stringer("pn", pair.pn).Logger()
	from, to, outcome := c.pickAltGhostHeal(lidGhost, pnGhost, pair)
	if outcome != healOutcomeHeal {
		evt := log.Info().Str("outcome", outcome)
		if lidGhost != nil && pnGhost != nil {
			evt = evt.Str("lid_ghost_name", lidGhost.Name).Str("pn_ghost_name", pnGhost.Name)
		}
		evt.Msg("Leaving alt ghost pair alone")
		return outcome
	}
	log = log.With().
		Str("from_ghost", string(from.ID)).
		Str("to_ghost", string(to.ID)).
		Str("old_name", to.Name).
		Str("new_name", from.Name).
		Logger()
	if !apply {
		log.Info().Msg("Would heal alt ghost name")
		return outcome
	}
	name := from.Name
	to.UpdateInfo(ctx, c.healAltGhostInfo(name, pair))
	if to.Name != name {
		log.Info().Str("current_name", to.Name).Msg("Alt ghost got a name meanwhile, left alone")
		return healOutcomeRenamed
	}
	log.Info().Bool("pushed", to.NameSet).Msg("Healed alt ghost name")
	return outcome
}

// pickAltGhostHeal returns the named twin and the nameless one to copy its name to.
func (c *Config) pickAltGhostHeal(lidGhost, pnGhost *bridgev2.Ghost, pair altGhostPair) (from, to *bridgev2.Ghost, outcome string) {
	if lidGhost == nil || pnGhost == nil {
		return nil, nil, healOutcomeMissingTwin
	}
	lidNameless := c.isNamelessRender(lidGhost.Name, pair)
	pnNameless := c.isNamelessRender(pnGhost.Name, pair)
	switch {
	case lidNameless && pnNameless:
		return nil, nil, healOutcomeBothNameless
	case !lidNameless && !pnNameless:
		return nil, nil, healOutcomeBothNamed
	case lidNameless:
		return pnGhost, lidGhost, healOutcomeHeal
	default:
		return lidGhost, pnGhost, healOutcomeHeal
	}
}

// healAltGhostInfo renames a ghost through the normal UpdateInfo path, re-checking under its
// syncLock that it is still nameless.
func (c *Config) healAltGhostInfo(name string, pair altGhostPair) *bridgev2.UserInfo {
	return &bridgev2.UserInfo{ExtraUpdates: func(ctx context.Context, ghost *bridgev2.Ghost) bool {
		if !c.isNamelessRender(ghost.Name, pair) {
			return false
		}
		return ghost.UpdateName(ctx, name)
	}}
}

// isNamelessRender reports whether name is what displayname_template renders for this contact
// without any contact name, the same test contactNamesGhost applies to a fresh render.
func (c *Config) isNamelessRender(name string, pair altGhostPair) bool {
	if name == "" {
		return true
	}
	phone := "+" + pair.pn.User
	for _, render := range []string{
		c.FormatDisplayname(pair.pn, phone, types.ContactInfo{}),
		c.FormatDisplayname(pair.lid, phone, types.ContactInfo{}),
		c.FormatDisplayname(pair.lid, "", types.ContactInfo{}),
	} {
		if name == render {
			return true
		}
	}
	// A ghost may carry a phone render this pair cannot rebuild, e.g. the server's redacted phone
	// on a LID ghost: match the template's text around .Phone or .RedactedPhone and a value made
	// only of phone characters. A push name of only digits counts as nameless too; the dry-run
	// list shows any such pair.
	const marker = "\x00"
	for _, probe := range []string{
		c.FormatDisplayname(types.EmptyJID, marker, types.ContactInfo{}),
		c.FormatDisplayname(types.EmptyJID, "", types.ContactInfo{RedactedPhone: marker}),
	} {
		prefix, suffix, ok := strings.Cut(probe, marker)
		if !ok || len(name) <= len(prefix)+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		if strings.Trim(name[len(prefix):len(name)-len(suffix)], "+0123456789∙ ") == "" {
			return true
		}
	}
	return false
}
