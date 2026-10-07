package spec

import "testing"

func TestValidateVaultPathExtraBaseDirs(t *testing.T) {
	ae := &AllowlistEnforcer{vaultBaseDir: "rs-manager/rs-manager/seam/routes"}
	ae.SetExtraVaultBaseDirs(ParseExtraVaultBaseDirs(" rs-manager/rs-manager/rackspace-spot/ , "))

	ok := []struct{ path, owner string }{
		{"rs-manager/rs-manager/seam/routes/unifi/api-key", "unifi"},
		{"rs-manager/rs-manager/rackspace-spot/apexalgo/api-key", "apexalgo"},
		{"rs-manager/rs-manager/rackspace-spot/apexalgo-agent/api-key", "apexalgo-agent"},
	}
	for _, c := range ok {
		if err := ae.ValidateVaultPath(c.path, c.owner); err != nil {
			t.Errorf("%s (%s) rejected: %v", c.path, c.owner, err)
		}
	}

	bad := []struct{ path, owner string }{
		// An owner must not claim a sibling whose name it prefixes.
		{"rs-manager/rs-manager/rackspace-spot/apexalgo-agent/api-key", "apexalgo"},
		{"rs-manager/rs-manager/rackspace-spot/apexalgo/api-key", "apexalgo-agent"},
		{"rs-manager/rs-manager/rackspace-spot/apexalgo", "apexalgo"},
		{"rs-manager/rs-manager/other/apexalgo/api-key", "apexalgo"},
		{"rs-manager/rs-manager/rackspace-spot/../seam/routes/x/k", "x"},
	}
	for _, c := range bad {
		if err := ae.ValidateVaultPath(c.path, c.owner); err == nil {
			t.Errorf("%s (%s) accepted, want rejection", c.path, c.owner)
		}
	}

	plain := &AllowlistEnforcer{vaultBaseDir: "rs-manager/rs-manager/seam/routes"}
	if err := plain.ValidateVaultPath("rs-manager/rs-manager/rackspace-spot/apexalgo/api-key", "apexalgo"); err == nil {
		t.Error("extra base accepted without being configured")
	}
}
