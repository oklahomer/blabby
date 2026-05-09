package connection

import "testing"

func TestTypeName(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "nil", in: nil, want: "<nil>"},
		{name: "int", in: 42, want: "int"},
		{name: "pointer", in: &struct{}{}, want: "*struct {}"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := typeName(tc.in); got != tc.want {
				t.Errorf("typeName(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
