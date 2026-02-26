package vibeflowcli

import "regexp"

// ErrorSeverity classifies how an error should be handled.
type ErrorSeverity int

const (
	// SeverityRecoverable means the error can be retried automatically.
	SeverityRecoverable ErrorSeverity = iota
	// SeverityFatal means the error is unrecoverable — notify only.
	SeverityFatal
)

// ErrorPattern represents a known error signature from an agent provider.
type ErrorPattern struct {
	Provider        string         // Provider key ("claude", "codex", "gemini") or "*" for universal.
	Regex           *regexp.Regexp // Compiled regex to match against captured output.
	Severity        ErrorSeverity  // Whether the error is recoverable or fatal.
	RecoveryMessage string         // Text to inject via SendKeys for recovery.
	RequiresBackoff bool           // True if rate-limit related (needs exponential backoff).
	Description     string         // Human-readable description of the error.
}

// ErrorPatternRegistry holds a collection of error patterns for matching
// against captured tmux pane output.
type ErrorPatternRegistry struct {
	patterns []ErrorPattern
}

// NewErrorPatternRegistry creates a registry with the default built-in patterns.
func NewErrorPatternRegistry() *ErrorPatternRegistry {
	return &ErrorPatternRegistry{patterns: DefaultPatterns()}
}

// Match scans the output (typically the last few lines of captured pane output)
// against all registered patterns for the given provider. Returns the first
// matching pattern, or nil if no match is found. Universal patterns ("*") are
// checked for all providers.
func (r *ErrorPatternRegistry) Match(provider, output string) *ErrorPattern {
	for i := range r.patterns {
		p := &r.patterns[i]
		if p.Provider != "*" && p.Provider != provider {
			continue
		}
		if p.Regex.MatchString(output) {
			return p
		}
	}
	return nil
}

// AddPattern adds a custom pattern to the registry.
func (r *ErrorPatternRegistry) AddPattern(p ErrorPattern) {
	r.patterns = append(r.patterns, p)
}

// DefaultPatterns returns the built-in error patterns for all supported providers.
func DefaultPatterns() []ErrorPattern {
	return []ErrorPattern{
		// --- Claude Code ---
		{
			Provider:        "claude",
			Regex:           regexp.MustCompile(`API Error:\s*5\d{2}`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "The previous API call failed with a server error. Please retry the last operation.",
			RequiresBackoff: false,
			Description:     "Claude API 5xx server error",
		},
		{
			Provider:        "claude",
			Regex:           regexp.MustCompile(`API Error:\s*529`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "The API is overloaded. Please wait a moment and retry the last operation.",
			RequiresBackoff: true,
			Description:     "Claude API overloaded (529)",
		},
		{
			Provider:        "claude",
			Regex:           regexp.MustCompile(`API Error:\s*429`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "Rate limit hit. Please wait and retry the last operation.",
			RequiresBackoff: true,
			Description:     "Claude API rate limit (429)",
		},
		{
			Provider:        "claude",
			Regex:           regexp.MustCompile(`(?i)connection\s+refused`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "Connection was refused. Please retry the last operation.",
			RequiresBackoff: true,
			Description:     "Claude connection refused",
		},
		{
			Provider:        "claude",
			Regex:           regexp.MustCompile(`(?i)\bETIMEDOUT\b|\btimed?\s*out\b`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "The request timed out. Please retry the last operation.",
			RequiresBackoff: true,
			Description:     "Claude connection timeout",
		},

		// --- OpenAI Codex CLI ---
		{
			Provider:        "codex",
			Regex:           regexp.MustCompile(`(?i)OpenAI\s+API\s+error`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "The OpenAI API returned an error. Please retry the last operation.",
			RequiresBackoff: false,
			Description:     "Codex API error",
		},
		{
			Provider:        "codex",
			Regex:           regexp.MustCompile(`(?i)rate\s+limit\s+exceeded`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "Rate limit exceeded. Please wait and retry the last operation.",
			RequiresBackoff: true,
			Description:     "Codex rate limit",
		},

		// --- Google Gemini CLI ---
		{
			Provider:        "gemini",
			Regex:           regexp.MustCompile(`RESOURCE_EXHAUSTED`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "Gemini resource quota exhausted. Please wait and retry the last operation.",
			RequiresBackoff: true,
			Description:     "Gemini resource exhausted",
		},
		{
			Provider:        "gemini",
			Regex:           regexp.MustCompile(`(?i)INTERNAL\s+server\s+error|google\.api.*INTERNAL`),
			Severity:        SeverityRecoverable,
			RecoveryMessage: "Gemini internal server error. Please retry the last operation.",
			RequiresBackoff: false,
			Description:     "Gemini internal error",
		},

		// --- Universal patterns (all providers) ---
		{
			Provider:        "*",
			Regex:           regexp.MustCompile(`^panic:`),
			Severity:        SeverityFatal,
			RecoveryMessage: "",
			RequiresBackoff: false,
			Description:     "Go panic (fatal)",
		},
		{
			Provider:        "*",
			Regex:           regexp.MustCompile(`^fatal error:`),
			Severity:        SeverityFatal,
			RecoveryMessage: "",
			RequiresBackoff: false,
			Description:     "Fatal error (fatal)",
		},
	}
}
