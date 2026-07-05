package discovery

import "testing"

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"Patricks-MacBook-Pro.local":  "Patricks-MacBook-Pro",
		"Patricks-MacBook-Pro.local.": "Patricks-MacBook-Pro",
		"homelab-r730":                "homelab-r730",
		"":                            "traego-controller",
		".local":                      "traego-controller",
	}
	for in, want := range cases {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
