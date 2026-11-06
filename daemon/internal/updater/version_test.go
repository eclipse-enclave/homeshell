package updater

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name               string
		candidate, current string
		want               bool
	}{
		{"patch bump", "1.2.4", "1.2.3", true},
		{"minor bump", "1.3.0", "1.2.9", true},
		{"major bump", "2.0.0", "1.9.9", true},
		{"equal", "1.2.3", "1.2.3", false},
		{"older", "1.2.2", "1.2.3", false},
		{"v prefix on both", "v1.2.4", "v1.2.3", true},
		{"v prefix mismatch", "1.2.4", "v1.2.3", true},
		{"dev current is always older", "0.0.1", "dev", true},
		{"empty current treated as older", "1.0.0", "", true},
		{"empty candidate is not newer", "", "1.0.0", false},
		{"prerelease is older than its stable release", "1.2.3-rc1", "1.2.3", false},
		{"stable is newer than its prerelease", "1.2.3", "1.2.3-rc1", true},
		{"newer prerelease", "1.2.3-rc2", "1.2.3-rc1", true},
		{"build metadata ignored", "1.2.4+build5", "1.2.3", true},
		{"shorter current", "1.2.1", "1.2", true},
		{"longer equal current", "1.2", "1.2.0", false},
		{"double-digit components", "1.10.0", "1.9.0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNewer(tc.candidate, tc.current); got != tc.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
			}
		})
	}
}
