package repos

// Settings are a linked repository's settings.
type Settings struct {
	DefaultBranch string `json:"default_branch"`
	AutoReview    bool   `json:"auto_review"`
}

// LinkedRepo is a git repository linked to the project.
type LinkedRepo struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Settings Settings `json:"settings"`
}
