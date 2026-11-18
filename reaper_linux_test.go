//go:build linux

package xexec

import "testing"

func TestZombieChildStat(t *testing.T) {
	r := &childReaper{}
	for _, tc := range []struct {
		name string
		stat string
		want bool
	}{
		{"zombie", "17 (worker) Z 42 1 1", true},
		{"complex-name", "17 (worker (nested) name) Z 42 1 1", true},
		{"running", "17 (worker) S 42 1 1", false},
		{"different-parent", "17 (worker) Z 43 1 1", false},
		{"bad-parent", "17 (worker) Z invalid", false},
		{"missing-fields", "17 (worker) Z", false},
		{"missing-name", "invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.zombieChild(tc.stat, 42); got != tc.want {
				t.Fatalf("zombieChild(%q)=%v; want %v", tc.stat, got, tc.want)
			}
		})
	}
}
