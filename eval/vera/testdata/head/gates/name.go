package gates

import "context"

// The status check names VibeFlow can post. Repositories set up before the
// rename require the legacy name in their branch protection rules; newer ones
// require the plain name.
const (
	PlainName  = "vibeflow"
	LegacyName = "VibeFlow / Vera"
)

// Rule is a branch's protection rule.
type Rule struct {
	RequiredChecks []string
}

// RuleReader reads branch protection rules from the git provider.
type RuleReader interface {
	BranchRule(ctx context.Context, repo, branch string) (Rule, error)
}

// Repo is a linked repository.
type Repo struct {
	FullName string
	// LegacyGateNames is set on repositories linked before the rename.
	LegacyGateNames bool
}

// CheckName is the status check name to post for a pull request into base:
// the name the base branch's protection rule requires, so the check can
// satisfy the rule, or the plain name when the rule requires neither.
func CheckName(ctx context.Context, rules RuleReader, repo Repo, base string) (string, error) {
	if repo.LegacyGateNames {
		return LegacyName, nil
	}
	rule, err := rules.BranchRule(ctx, repo.FullName, base)
	if err != nil {
		return "", err
	}
	for _, c := range rule.RequiredChecks {
		if c == PlainName || c == LegacyName {
			return c, nil
		}
	}
	return PlainName, nil
}
