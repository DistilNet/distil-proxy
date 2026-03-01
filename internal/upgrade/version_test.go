package upgrade

import "testing"

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{current: "1.0.0", latest: "1.0.1", want: true},
		{current: "1.0.1", latest: "1.0.0", want: false},
		{current: "v1.2.3", latest: "1.3.0", want: true},
		{current: "1.2.3", latest: "1.2.3", want: false},
		{current: "bad", latest: "1.0.0", want: false},
	}

	for _, test := range tests {
		got := isNewerVersion(test.current, test.latest)
		if got != test.want {
			t.Fatalf("isNewerVersion(%q, %q) = %v, want %v", test.current, test.latest, got, test.want)
		}
	}
}
