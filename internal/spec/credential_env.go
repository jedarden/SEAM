package spec

import (
	"fmt"
	"strings"
)

// credentialEnvOwnerError checks the env credential namespace against the
// fragment owner. The owner directory is authoritative; sibling owners are
// considered together so an owner such as "ab" cannot claim the longer
// "ab-cd" namespace after hyphens are mapped to underscores.
func credentialEnvOwnerError(name, owner string, owners []string) error {
	prefix := credentialEnvPrefix(owner)
	if !strings.HasPrefix(name, prefix) {
		return fmt.Errorf("x-credential-env %q must begin with %s", name, prefix)
	}
	remainder := strings.TrimPrefix(name, prefix)
	if remainder == "" {
		return fmt.Errorf("x-credential-env %q must include a name after %s", name, prefix)
	}
	for _, r := range remainder {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return fmt.Errorf("x-credential-env %q suffix must match [A-Z0-9_]+", name)
		}
	}
	for _, sibling := range owners {
		if sibling == owner {
			continue
		}
		siblingPrefix := credentialEnvPrefix(sibling)
		if len(siblingPrefix) > len(prefix) && strings.HasPrefix(name, siblingPrefix) {
			return fmt.Errorf("x-credential-env %q belongs to the more specific owner %q", name, sibling)
		}
	}
	return nil
}

func credentialEnvPrefix(owner string) string {
	return "SEAM_CRED_" + strings.ToUpper(strings.ReplaceAll(owner, "-", "_")) + "_"
}
