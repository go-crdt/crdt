package crdt

import (
	"fmt"
	"testing"
)

// A site identity derived from an eduGAIN assertion.
//
// Federation over GÉANT means the identity half is already solved by eduGAIN:
// every person arrives carrying an identifier that is globally unique and
// scoped to the institution that issued it — an eduPersonPrincipalName
// (dedelavennat@cnrs.fr), a subject-id, or an OIDC issuer and sub. Two
// instances that have never spoken cannot mint the same one, because the scope
// is the home organisation and only it issues within its own.
//
// So the question for this package is narrow and answerable: does DeriveSiteID
// turn those identifiers into distinct sites at the scale eduGAIN actually has?
// A collision would be two operations claiming one identity, which is the one
// thing the whole design rests on not happening.
func TestDeriveSiteIDAtEduGAINScale(t *testing.T) {
	// eduGAIN interfederates on the order of a few thousand entities; the
	// people behind them are tens of millions. This takes twenty million
	// across four thousand scopes, which is more of both than Europe has.
	const (
		people = 20000000
		scopes = 4000
	)
	seen := make(map[SiteID]string, people)
	collisions := 0
	var first [2]string
	for i := range people {
		// The shape a SAML assertion hands over.
		id := fmt.Sprintf("u%d@inst%d.ac.example", i, i%scopes)
		site := DeriveSiteID([]byte(id))
		if prev, clash := seen[site]; clash {
			collisions++
			if first[0] == "" {
				first[0], first[1] = prev, id
			}
			continue
		}
		seen[site] = id
	}
	// What a uniform 64-bit hash would give: n^2 / 2^65.
	expected := float64(people) * float64(people) / 36893488147419103232.0
	t.Logf("%d identifiers over %d scopes: %d collisions (a uniform 64-bit hash gives %.4f)",
		people, scopes, collisions, expected)
	if first[0] != "" {
		t.Logf("   for instance %q and %q would share a site", first[0], first[1])
	}
	if collisions > 0 {
		t.Errorf("%d collisions: an identity two people share is two operations "+
			"claiming to be one", collisions)
	}
}

// And the case a caller must NOT reach for: a bare local identifier, with no
// scope. Two instances with a user called "42" then mint the same site.
func TestABareIdentifierIsNotFederationSafe(t *testing.T) {
	paris := DeriveSiteID([]byte("42"))
	lyon := DeriveSiteID([]byte("42"))
	if paris != lyon {
		t.Fatal("the same bytes hashed to two sites, which is not how a hash works")
	}
	t.Logf("a bare %q is site %d on every instance that derives it", "42", paris)
	// Scoped, they are two people and two sites.
	scopedParis := DeriveSiteID([]byte("42@paris.ac.example"))
	scopedLyon := DeriveSiteID([]byte("42@lyon.ac.example"))
	if scopedParis == scopedLyon {
		t.Fatal("two scopes hashed to one site")
	}
	t.Logf("scoped, they are %d and %d", scopedParis, scopedLyon)
}
