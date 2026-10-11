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
	return credentialNameOwnerError("x-credential-env", name, owner, owners)
}

func credentialFileOwnerError(name, owner string, owners []string) error {
	return credentialNameOwnerError("x-credential-file", name, owner, owners)
}

func credentialNameOwnerError(field, name, owner string, owners []string) error {
	prefix := credentialEnvPrefix(owner)
	if !strings.HasPrefix(name, prefix) {
		return fmt.Errorf("%s %q must begin with %s", field, name, prefix)
	}
	remainder := strings.TrimPrefix(name, prefix)
	if remainder == "" {
		return fmt.Errorf("%s %q must include a name after %s", field, name, prefix)
	}
	for _, r := range remainder {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return fmt.Errorf("%s %q suffix must match [A-Z0-9_]+", field, name)
		}
	}
	for _, sibling := range owners {
		if sibling == owner {
			continue
		}
		siblingPrefix := credentialEnvPrefix(sibling)
		if len(siblingPrefix) > len(prefix) && strings.HasPrefix(name, siblingPrefix) {
			return fmt.Errorf("%s %q belongs to the more specific owner %q", field, name, sibling)
		}
	}
	return nil
}

func credentialEnvPrefix(owner string) string {
	return "SEAM_CRED_" + strings.ToUpper(strings.ReplaceAll(owner, "-", "_")) + "_"
}
