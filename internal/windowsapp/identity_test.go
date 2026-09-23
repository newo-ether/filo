package windowsapp

import "testing"

func TestOwnerRequiresExactOrdinaryAccountAndInteractiveOriginalFamily(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	for _, test := range []struct {
		name, expected, actual, family string
		session                        uint32
		valid                          bool
	}{
		{"original", sid, sid, desktopFamily, 1, true},
		{"wrong account", sid, "S-1-5-21-1-2-3-1002", desktopFamily, 1, false},
		{"system", "S-1-5-18", "S-1-5-18", desktopFamily, 1, false},
		{"empty account", "", "", desktopFamily, 1, false},
		{"service session", sid, sid, desktopFamily, 0, false},
		{"other publisher", sid, sid, "OpenAI.Codex_other", 1, false},
		{"other app", sid, sid, "Other_2p2nqsd0c76g0", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := checkOwner(test.expected, test.actual, test.family, test.session); (err == nil) != test.valid {
				t.Fatalf("Identity accepted=%v: %v", err == nil, err)
			}
		})
	}
}
