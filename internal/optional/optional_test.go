package optional

import "testing"

func TestClone(t *testing.T) {
	if Clone[string](nil) != nil {
		t.Fatal("Clone(nil) is not nil")
	}
	value := "tr1_current"
	cloned := Clone(&value)
	if cloned == &value || *cloned != value {
		t.Fatalf("Clone() = %p %q, want a copy of %q", cloned, *cloned, value)
	}
}

func TestEqual(t *testing.T) {
	a, b, c := "a", "a", "c"
	for _, tc := range []struct {
		left, right *string
		want        bool
	}{
		{nil, nil, true},
		{&a, nil, false},
		{nil, &a, false},
		{&a, &b, true},
		{&a, &c, false},
	} {
		if got := Equal(tc.left, tc.right); got != tc.want {
			t.Fatalf("Equal(%v, %v) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
}
