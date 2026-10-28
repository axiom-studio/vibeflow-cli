package webhooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"example.com/reviewfixture/repos"
)

// Provider registers a webhook's signing secret with the git provider.
type Provider interface {
	SetWebhookSecret(ctx context.Context, repoName, secret string) error
}

// EnsureSigningSecret gives the linked repository a webhook signing secret
// when it has none, registers it with the provider and returns it. It runs on
// a repository's first webhook delivery and from the settings page's
// "Reconnect webhook" button, so calls for one repository can overlap.
func EnsureSigningSecret(ctx context.Context, store *repos.Store, p Provider, id int64) (string, error) {
	repo, err := store.Get(id)
	if err != nil {
		return "", err
	}
	if repo.Settings.WebhookSecret != "" {
		return repo.Settings.WebhookSecret, nil
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	if err := p.SetWebhookSecret(ctx, repo.Name, secret); err != nil {
		return "", err
	}
	repo.Settings.WebhookSecret = secret
	store.Put(repo)
	return secret, nil
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
