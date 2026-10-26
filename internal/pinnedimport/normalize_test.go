package pinnedimport_test

import (
	"testing"

	"shepherd/internal/pinnedimport"
)

func TestNormalizeRepositoryEquivalentSpellings(t *testing.T) {
	cases := map[string][]string{
		"https://git.example.com/team/modules": {
			"https://git.example.com/team/modules",
			"https://git.example.com/team/modules.git",
			"https://git.example.com/team/modules/",
			"https://git.example.com/team/modules.git/",
			"HTTPS://Git.Example.COM/team/modules.git",
			"https://git.example.com:443/team/modules.git",
		},
		"ssh://git@git.example.com/team/modules": {
			"ssh://git@git.example.com/team/modules.git",
			"ssh://git@git.example.com:22/team/modules",
			"git@git.example.com:team/modules.git",
			"git@GIT.example.com:team/modules",
			"SSH://git@git.example.com/team/modules/",
		},
		"http://git.example.com:8080/team/modules": {
			"http://git.example.com:8080/team/modules.git",
		},
		"ssh://git@[2001:db8::1]:2222/team/modules": {
			"ssh://git@[2001:DB8::1]:2222/team/modules.git",
		},
	}
	for want, spellings := range cases {
		for _, raw := range spellings {
			got, err := pinnedimport.NormalizeRepository(raw)
			if err != nil {
				t.Errorf("NormalizeRepository(%q): %v", raw, err)
				continue
			}
			if got != want {
				t.Errorf("NormalizeRepository(%q) = %q, want %q", raw, got, want)
			}
		}
	}
}

// The parts that may name another repository stay distinct: the path's case
// (most git servers are case-sensitive), the scheme, a non-default port and
// the ssh user.
func TestNormalizeRepositoryKeepsDistinctRepositoriesDistinct(t *testing.T) {
	base, err := pinnedimport.NormalizeRepository("https://git.example.com/team/modules")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://git.example.com/Team/modules",
		"http://git.example.com/team/modules",
		"https://git.example.com:8443/team/modules",
		"https://git.example.com/team/modules2",
		"https://git.example.com/team",
		"https://other.example.com/team/modules",
		"ssh://git@git.example.com/team/modules",
	} {
		got, err := pinnedimport.NormalizeRepository(raw)
		if err != nil {
			t.Errorf("NormalizeRepository(%q): %v", raw, err)
			continue
		}
		if got == base {
			t.Errorf("NormalizeRepository(%q) = %q, the same as a different repository", raw, got)
		}
	}
	a, errA := pinnedimport.NormalizeRepository("ssh://git@git.example.com/team/modules")
	b, errB := pinnedimport.NormalizeRepository("ssh://deploy@git.example.com/team/modules")
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a == b {
		t.Errorf("different ssh users normalised to the same form %q", a)
	}
}

func TestNormalizeRepositoryRefusesAmbiguousForms(t *testing.T) {
	for _, raw := range []string{
		"",
		" https://git.example.com/team/modules",
		"https://git.example.com/team/modules ",
		"https://git.example.com/team/mod%75les",
		"https://git.example.com/team/modules?ref=x",
		"https://git.example.com/team/modules#frag",
		"https://git.example.com/team//modules",
		"https://git.example.com/team/./modules",
		"https://git.example.com/team/../modules",
		"https://git.example.com/team\\modules",
		"https://user:secret@git.example.com/team/modules",
		"https://user@git.example.com/team/modules",
		"ssh://git:secret@git.example.com/team/modules",
		"git@git.example.com:~team/modules",
		"git@git.example.com:/team/modules",
		"https://git.example.com/",
		"https://git.example.com/.git",
		"file:///srv/repos/modules.git",
		"/srv/repos/modules.git",
		"../modules",
		"git://git.example.com/team/modules",
		"ftp://git.example.com/team/modules",
		// A non-ASCII host has punycode and Unicode spellings (and
		// look-alikes): refused, write the punycode.
		"https://gït.example.com/team/modules",
		"git@exämple.com:team/modules",
		"https://git.example.com/tëam/modules",
	} {
		if got, err := pinnedimport.NormalizeRepository(raw); err == nil {
			t.Errorf("NormalizeRepository(%q) = %q, want a refusal", raw, got)
		}
	}
}
