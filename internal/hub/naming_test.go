package hub

import "testing"

func TestNextSessionName(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		dir      string
		want     string
	}{
		{"fresh", nil, "/home/x/proj", "proj"},
		{"maps forbidden chars", nil, "/home/x/my.repo:v2", "my-repo-v2"},
		{"taken once", []string{"roost", "proj"}, "/home/x/proj", "proj-2"},
		{"taken twice", []string{"proj", "proj-2"}, "/home/x/proj", "proj-3"},
		{"suffix free", []string{"proj-2"}, "/home/x/proj", "proj"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NextSessionName(c.existing, c.dir); got != c.want {
				t.Fatalf("NextSessionName(%v, %q) = %q, want %q",
					c.existing, c.dir, got, c.want)
			}
		})
	}
}
