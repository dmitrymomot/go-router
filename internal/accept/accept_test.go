package accept

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		accept, offer string
		rank          int
		q             float64
	}{
		{"text/html", "text/html", 2, 1},
		{"Text/HTML;Q=0.5", "text/html", 2, 0.5},
		{"text/*;q=0.4", "text/html", 1, 0.4},
		{"*/*;q=0.2", "text/html", 0, 0.2},
		{"text/html;q=0, */*", "text/html", 2, 0},
		{"text/html;q=abc, */*", "text/html", 2, 0},
		{"text/html;q=2", "text/html", 2, 0},
		{"application/json", "text/html", -1, 0},
		{"text/html;q=0.3, text/html;q=0.7", "text/html", 2, 0.7},
		{"text/html", "nonsense", -1, 0},
	}
	for _, tc := range tests {
		rank, q := Match(tc.accept, tc.offer)
		if rank != tc.rank || q != tc.q {
			t.Errorf("Match(%q, %q) = %d, %v; want %d, %v", tc.accept, tc.offer, rank, q, tc.rank, tc.q)
		}
	}
}
