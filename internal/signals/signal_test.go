package signals

import "testing"

func TestSetSorted(t *testing.T) {
	// Sorted must always follow All's fixed order regardless of insertion
	// order, since error messages and String() depend on it being stable.
	s := NewSet(Profiles, Metrics, Traces, Logs)
	got := s.Sorted()
	want := []Signal{Metrics, Logs, Traces, Profiles}
	if len(got) != len(want) {
		t.Fatalf("Sorted() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Sorted()[%d] = %q, want %q (full: got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

func TestSetHasOnNil(t *testing.T) {
	var s Set
	if s.Has(Metrics) {
		t.Fatalf("nil Set reported Has(Metrics) = true")
	}
	if !s.Empty() {
		t.Fatalf("nil Set reported Empty() = false")
	}
}

func TestSetAddOnNil(t *testing.T) {
	var s Set
	s = s.Add(Metrics)
	if !s.Has(Metrics) {
		t.Fatalf("Add on nil Set did not produce a Set containing the added signal")
	}
}

func TestSetUnion(t *testing.T) {
	a := NewSet(Metrics, Logs)
	b := NewSet(Logs, Traces)
	got := a.Union(b)
	want := NewSet(Metrics, Logs, Traces)
	if !got.Equal(want) {
		t.Fatalf("Union = %s, want %s", got, want)
	}
	// inputs must not be mutated
	if !a.Equal(NewSet(Metrics, Logs)) {
		t.Fatalf("Union mutated its receiver: a = %s", a)
	}
}

func TestSetEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b Set
		want bool
	}{
		{"both empty", Set{}, nil, true},
		{"same members different order built", NewSet(Metrics, Logs), NewSet(Logs, Metrics), true},
		{"different size", NewSet(Metrics), NewSet(Metrics, Logs), false},
		{"same size different members", NewSet(Metrics), NewSet(Logs), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.want {
				t.Fatalf("(%s).Equal(%s) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestSetString(t *testing.T) {
	got := NewSet(Traces, Metrics).String()
	want := "[metrics,traces]"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if (Set{}).String() != "[]" {
		t.Fatalf("empty Set String() = %q, want %q", (Set{}).String(), "[]")
	}
}
