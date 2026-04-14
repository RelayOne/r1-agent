package plan

import (
	"testing"
)

func TestDetectSuppressesPlaceholderContext(t *testing.T) {
	// Classic Sentinel-style SOW: Guesty is the real integration, the
	// others are explicitly labeled "Coming Soon" placeholder cards.
	sow := &SOW{
		Sessions: []Session{
			{
				ID: "S7",
				Tasks: []Task{
					{
						ID: "T40",
						Description: `Build the integrations grid. Guesty is functional — wire up the credential form, endpoint call, and listing mapping. Hostaway, Mews, PointClickCare, Yardi, and RealPage are placeholder cards with a "Coming Soon" overlay — do not implement any API calls for these.`,
					},
				},
			},
		},
	}
	got := DetectExternalServices(sow, "")
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Name] = true
	}
	if !seen["guesty"] {
		t.Fatal("guesty is a real integration; must be detected")
	}
	for _, suppressed := range []string{"hostaway", "mews", "pointclickcare", "yardi", "realpage"} {
		if seen[suppressed] {
			t.Fatalf("%s is labeled Coming Soon placeholder; must be suppressed", suppressed)
		}
	}
}

func TestDetectStillFlagsWhenNotPlaceholder(t *testing.T) {
	// Same services, but without placeholder context — all must be
	// flagged. This is the regression guard that placeholder
	// suppression doesn't accidentally silence real integration requirements.
	sow := &SOW{
		Sessions: []Session{
			{Tasks: []Task{
				{ID: "T1", Description: "Wire up the Hostaway API for listing sync"},
				{ID: "T2", Description: "Connect to Mews for reservation push"},
			}},
		},
	}
	got := DetectExternalServices(sow, "")
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.Name] = true
	}
	if !seen["hostaway"] || !seen["mews"] {
		t.Fatalf("real integrations should still be detected; got %v", seen)
	}
}
