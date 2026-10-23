package connector

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridgev2"
)

var testPair = altGhostPair{
	lid: types.NewJID("20000000002", types.HiddenUserServer),
	pn:  types.NewJID("491700000001", types.DefaultUserServer),
}

func healTestConfig(t *testing.T) *Config {
	cfg := &Config{DisplaynameTemplate: defaultTemplate}
	if err := cfg.PostProcess(); err != nil {
		t.Fatalf("PostProcess() failed: %v", err)
	}
	return cfg
}

func TestIsNamelessRender(t *testing.T) {
	cfg := healTestConfig(t)
	tests := []struct {
		name string
		want bool
	}{
		{"", true},
		{"+491700000001 (WA)", true},
		{"Unknown user (WA)", true},
		{"+49∙∙∙∙∙∙∙∙01 (WA)", true},
		{"+1∙∙∙01 (WA)", true},
		{"Push (WA)", false},
		{"+49 Bakery∙ (WA)", false},
		{"+491700000009 (WA)", true},
		{"20000000002 (WA)", true},
		{"+49 170 000 (WA)", true},
		{"∙ (WA) extra", false},
	}
	for _, tc := range tests {
		if got := cfg.isNamelessRender(tc.name, testPair); got != tc.want {
			t.Errorf("isNamelessRender(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPickAltGhostHeal(t *testing.T) {
	cfg := healTestConfig(t)
	const phone = "+491700000001 (WA)"
	const realName = "Push (WA)"
	tests := []struct {
		name        string
		lid, pn     *bridgev2.Ghost
		wantOutcome string
		wantFrom    string
		wantTo      string
	}{
		{"LID named, phone nameless", newTestGhost(realName, true), newTestGhost(phone, true), healOutcomeHeal, "lid", "pn"},
		{"phone named, LID nameless", newTestGhost("+49∙∙∙∙∙∙∙∙01 (WA)", true), newTestGhost(realName, true), healOutcomeHeal, "pn", "lid"},
		{"phone named, LID empty", newTestGhost("", false), newTestGhost(realName, true), healOutcomeHeal, "pn", "lid"},
		{"both named", newTestGhost(realName, true), newTestGhost("Other (WA)", true), healOutcomeBothNamed, "", ""},
		{"both nameless", newTestGhost("+49∙∙∙∙∙∙∙∙01 (WA)", true), newTestGhost(phone, true), healOutcomeBothNameless, "", ""},
		{"missing LID twin", nil, newTestGhost(realName, true), healOutcomeMissingTwin, "", ""},
		{"missing phone twin", newTestGhost(realName, true), nil, healOutcomeMissingTwin, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ghosts := map[string]*bridgev2.Ghost{"lid": tc.lid, "pn": tc.pn, "": nil}
			from, to, outcome := cfg.pickAltGhostHeal(tc.lid, tc.pn, testPair)
			if outcome != tc.wantOutcome || from != ghosts[tc.wantFrom] || to != ghosts[tc.wantTo] {
				t.Errorf("got outcome %q, from %v, to %v; want %q, %s -> %s", outcome, from, to, tc.wantOutcome, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

func TestHealAltGhostInfo(t *testing.T) {
	cfg := healTestConfig(t)
	const realName = "Push (WA)"
	tests := []struct {
		name       string
		current    string
		wantName   string
		wantPushes int
	}{
		{"nameless twin gets the name", "+491700000001 (WA)", realName, 1},
		{"twin named meanwhile keeps its name", "Newer (WA)", "Newer (WA)", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ghost := newTestGhost(tc.current, true)
			applyName(&sync.Mutex{}, ghost, cfg.healAltGhostInfo(realName, testPair))
			if ghost.Name != tc.wantName || !ghost.NameSet {
				t.Errorf("ghost name = %q (set %v), want %q", ghost.Name, ghost.NameSet, tc.wantName)
			}
			if pushes := ghost.Intent.(*nameRecorder).pushes; pushes != tc.wantPushes {
				t.Errorf("displayname pushes = %d, want %d", pushes, tc.wantPushes)
			}
		})
	}
}

// A dry run must not touch the ghosts. They have no Bridge, so an UpdateInfo call would panic.
func TestHealAltGhostPairDryRun(t *testing.T) {
	cfg := healTestConfig(t)
	lid := newTestGhost("Push (WA)", true)
	pn := newTestGhost("+491700000001 (WA)", true)
	ctx := zerolog.Nop().WithContext(context.Background())
	if outcome := cfg.healAltGhostPair(ctx, lid, pn, testPair, false); outcome != healOutcomeHeal {
		t.Errorf("outcome = %q, want %q", outcome, healOutcomeHeal)
	}
	if pn.Name != "+491700000001 (WA)" || lid.Name != "Push (WA)" || pn.Intent.(*nameRecorder).pushes != 0 {
		t.Errorf("dry run changed a ghost: lid %q, pn %q, pushes %d", lid.Name, pn.Name, pn.Intent.(*nameRecorder).pushes)
	}
}
