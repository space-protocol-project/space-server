package home

import "testing"

func TestPostgresMajorVersionGuard(t *testing.T) {
	for _, test := range []struct {
		binary, cluster string
		allowed         bool
	}{
		{"postgres (PostgreSQL) 17.11", "17\n", true},
		{"postgres (PostgreSQL) 17.11", "", true},
		{"postgres (PostgreSQL) 17.6", "17", true},
		{"postgres (PostgreSQL) 17.11", "16", false},
		{"postgres (PostgreSQL) 18.1", "17", false},
		{"postgres (PostgreSQL) 17.11", "garbage", false},
		{"unknown", "17", false},
	} {
		if err := validatePostgresVersions(test.binary, test.cluster); (err == nil) != test.allowed {
			t.Errorf("%q / %q: %v", test.binary, test.cluster, err)
		}
	}
}
