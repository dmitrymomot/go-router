package middleware

import "testing"

func TestKeptProtoAllocatesNothing(t *testing.T) {
	values := []string{"http, https ", " https,http"}
	for _, leftmost := range []bool{false, true} {
		allocs := testing.AllocsPerRun(100, func() {
			if proto, _ := keptProto(leftmost, "", values); proto == "" {
				t.Fatal("keptProto found no scheme")
			}
		})
		if allocs != 0 {
			t.Errorf("keptProto(leftmost=%v) allocated %v times, want 0", leftmost, allocs)
		}
	}
}

func TestFirstEntry(t *testing.T) {
	values := []string{" , a, b", "c ,d, "}
	if got := firstEntry(values, true); got != "a" {
		t.Errorf("leftmost = %q, want a", got)
	}
	if got := firstEntry(values, false); got != "d" {
		t.Errorf("rightmost = %q, want d", got)
	}
	if got := firstEntry([]string{" , "}, false); got != "" {
		t.Errorf("empty = %q, want none", got)
	}
}
