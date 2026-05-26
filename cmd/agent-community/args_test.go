package main

import (
	"flag"
	"reflect"
	"testing"
)

func makeFS() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("theme", "", "")
	fs.String("path", "", "")
	fs.Bool("force", false, "")
	return fs
}

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "positional then flag-with-space-value",
			in:   []string{"my-community", "--theme", "cheeses"},
			want: []string{"--theme", "cheeses", "my-community"},
		},
		{
			name: "positional then flag-with-equals",
			in:   []string{"my-community", "--theme=cheeses"},
			want: []string{"--theme=cheeses", "my-community"},
		},
		{
			name: "flag then positional (already correct)",
			in:   []string{"--theme", "cheeses", "my-community"},
			want: []string{"--theme", "cheeses", "my-community"},
		},
		{
			name: "bool flag does not consume next arg",
			in:   []string{"my-community", "--force", "--theme=jazz"},
			want: []string{"--force", "--theme=jazz", "my-community"},
		},
		{
			name: "double-dash terminator preserves order after",
			in:   []string{"--theme", "x", "--", "--theme", "y"},
			want: []string{"--theme", "x", "--theme", "y"},
		},
		{
			name: "multiple positionals",
			in:   []string{"a", "--theme", "x", "b", "c"},
			want: []string{"--theme", "x", "a", "b", "c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderArgs(makeFS(), tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reorderArgs(%v) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}
