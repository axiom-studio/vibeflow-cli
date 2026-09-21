package vibeflowcli

// The routing matrix: every built-in harness crossed with every routing mode,
// declared as data so the support story is asserted by tests and rendered into
// docs from one place instead of being retyped into a table that goes stale.
//
// The matrix declares INTENT. The builders in routing.go and config.go are the
// TRUTH. TestRoutingMatrix asserts the two agree, so neither can drift:
//
//   - a cell claiming Supported must actually produce session env
//   - a cell claiming Gap or Unsupported must actually produce none
//   - every variable a builder emits must be declared on its cell
//
// The last one is the load-bearing assertion. Adding a variable to a builder
// without declaring it here fails the test, which is what turns a silent
// credential-leak regression into a red build.

import (
	"fmt"
	"sort"
	"strings"
)

// CellStatus is how a harness/routing pair is expected to behave.
type CellStatus int

const (
	// Unsupported means the harness cannot do this routing mode by design —
	// it talks only to its own backend. Not a gap, and not something to fix.
	Unsupported CellStatus = iota
	// Gap means the mode should work for this harness but is not wired yet.
	// Every Gap carries a Ticket.
	Gap
	// Supported means the mode is wired and the builders emit session env.
	Supported
)

// String renders the status for the generated document.
func (s CellStatus) String() string {
	switch s {
	case Supported:
		return "supported"
	case Gap:
		return "gap"
	default:
		return "unsupported"
	}
}

// RoutingCell declares one harness × routing-mode pair.
type RoutingCell struct {
	Provider string // provider registry key
	Routing  string // RoutingDirect / RoutingGateway / RoutingEndpoint / RoutingShell

	Status CellStatus
	Reason string // why, for Unsupported and Gap. Shown in the document.
	Ticket string // tracking item, required for Gap

	// WireFormat is the API the far end must speak, for the modes that point
	// a harness at someone else's server.
	WireFormat string

	// RequiresEnv names variables the builder must set to a non-empty value.
	RequiresEnv []string
	// BlanksEnv names variables the builder must set to an EMPTY value. These
	// are the leak guards: a value exported in the user's shell is inherited
	// through the tmux server, so the only way to stop it reaching the far end
	// is to set the variable explicitly to "".
	BlanksEnv []string
	// ForwardsEnv names variables the mode passes through from the user's
	// shell when they are exported, rather than setting a value of its own.
	// Shell routing forwards the harness's own credentials this way, so these
	// DO reach the endpoint — they are the opposite of BlanksEnv and must be
	// declared for the reader to see the real credential flow.
	ForwardsEnv []string
	// DynamicEnvPrefixes names variables whose full name is computed at build
	// time (the qwen custom-API-key binding encodes the endpoint URL into the
	// variable name), so they can be accounted for without being literals.
	DynamicEnvPrefixes []string

	// Caveat is a safety note shown as a footnote under the table, for a cell
	// whose correct behaviour still has a consequence worth reading before
	// choosing it.
	Caveat string

	// Flags are launch-flag fragments the mode appends to the command.
	Flags []string

	// VerifiedVersion and VerifiedOn record a live run against the real
	// harness. Unit tests prove the right variables are set; only a live run
	// proves the far end accepts them. Empty means not yet live-verified.
	VerifiedVersion string
	VerifiedOn      string // YYYY-MM-DD
}

// RoutingModes lists every routing mode, in the order the document renders
// them. Kept next to the matrix so a new mode is one edit away from being
// required on all 7 harnesses by TestRoutingMatrixIsExhaustive.
var RoutingModes = []string{RoutingDirect, RoutingGateway, RoutingEndpoint, RoutingShell}

// Reasons shared by the harnesses that connect only to their own backend, so
// the same explanation is not reworded per cell.
const (
	reasonCursorOwnBackend = "Cursor Agent connects only to its own backend"
	reasonKiroOwnBackend   = "Kiro authenticates with its own KIRO_API_KEY against its own backend"
)

// codexEndpointFlagFragments are the parts of Codex's -c model-provider flags
// that must survive sh-quoting. Both of Codex's non-direct modes use them,
// because Codex ignores OPENAI_BASE_URL and takes its endpoint only this way.
//
// env_key is the load-bearing fragment: it names the variable Codex reads the
// key FROM, which is what keeps the key in the environment instead of argv.
var codexEndpointFlagFragments = []string{
	`model_provider="` + codexEndpointProviderID + `"`,
	"model_providers." + codexEndpointProviderID + ".base_url=",
	"model_providers." + codexEndpointProviderID + `.env_key="OPENAI_API_KEY"`,
	"model_providers." + codexEndpointProviderID + `.wire_api="responses"`,
}

// RoutingMatrix declares all 7 harnesses × 4 routing modes.
//
// Direct is supported everywhere: it is the absence of redirection, so it
// needs no wiring. Its BlanksEnv entries are the vars ClearLLMGatewayEnv and
// ClearShellEndpointEnv blank when the user explicitly chooses direct, so a
// gateway or endpoint left in the shell cannot quietly stay in effect.
var RoutingMatrix = []RoutingCell{
	// ---- claude ------------------------------------------------------
	{
		Provider: "claude", Routing: RoutingDirect, Status: Supported,
		BlanksEnv: []string{"ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_BASE_URL"},
	},
	{
		Provider: "claude", Routing: RoutingGateway, Status: Supported,
		WireFormat: "Anthropic-compatible, Messages API",
		// The gateway key rides in a custom header so the standard auth
		// headers stay free for the user's own OAuth token.
		RequiresEnv: []string{"ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_BASE_URL"},
	},
	{
		Provider: "claude", Routing: RoutingEndpoint, Status: Supported,
		WireFormat: "Anthropic-compatible, Messages API",
		// Every model tier is pinned to the endpoint's model so background
		// and sub-agent calls never ask a third party for a Claude model.
		RequiresEnv: []string{
			"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
		},
		// AUTH_TOKEN is always set, so blanking API_KEY and the gateway
		// header is what stops the subscription login reaching the endpoint.
		BlanksEnv: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS"},
	},
	{
		Provider: "claude", Routing: RoutingShell, Status: Supported,
		WireFormat:  "Anthropic-compatible, Messages API",
		RequiresEnv: []string{"ANTHROPIC_BASE_URL"},
		// Passed through to the endpoint when exported — including the
		// credential. With neither token set, Claude Code sends the
		// subscription login instead, which is why the launch paths warn.
		ForwardsEnv: []string{
			"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
		},
		Caveat: "If neither `ANTHROPIC_AUTH_TOKEN` nor `ANTHROPIC_API_KEY` is set, Claude Code sends your " +
			"subscription login to this URL instead. The wizard warns about this and does not pre-select " +
			"the detected endpoint in that case, and restart and quick-switch warn too.",
	},

	// ---- codex -------------------------------------------------------
	{
		Provider: "codex", Routing: RoutingDirect, Status: Supported,
		BlanksEnv: []string{"OPENAI_BASE_URL"},
	},
	{
		Provider: "codex", Routing: RoutingGateway, Status: Supported,
		WireFormat:  "OpenAI-compatible",
		RequiresEnv: []string{"GATEWAY_API_KEY", "OPENAI_BASE_URL"},
	},
	{
		Provider: "codex", Routing: RoutingEndpoint, Status: Supported,
		WireFormat: "OpenAI-compatible, Responses API",
		// Codex reads the endpoint from -c model_provider flags, not the env.
		// Only the key comes through the env, via the provider's env_key.
		RequiresEnv: []string{"OPENAI_API_KEY"},
		// Blanked so the gateway provider flags stay off.
		BlanksEnv: []string{"OPENAI_BASE_URL"},
		Flags:     codexEndpointFlagFragments,
	},
	{
		Provider: "codex", Routing: RoutingShell, Status: Supported,
		WireFormat: "OpenAI-compatible, Responses API",
		// The counter-intuitive cell: OPENAI_BASE_URL is the variable the
		// user exports to configure codex, and shell routing BLANKS it. Codex
		// ignores OPENAI_BASE_URL natively, so the URL is passed via the same
		// -c flags as endpoint routing and the variable is cleared to keep
		// the gateway provider flags off.
		RequiresEnv: []string{"OPENAI_API_KEY"},
		BlanksEnv:   []string{"OPENAI_BASE_URL"},
		// The shell's own key is passed through as the endpoint credential;
		// when none is exported the keyless placeholder is used instead.
		ForwardsEnv: []string{"OPENAI_API_KEY"},
		Flags:       codexEndpointFlagFragments,
	},

	// ---- gemini ------------------------------------------------------
	{
		Provider: "gemini", Routing: RoutingDirect, Status: Supported,
		BlanksEnv: []string{"GOOGLE_GEMINI_BASE_URL"},
	},
	{
		Provider: "gemini", Routing: RoutingGateway, Status: Supported,
		WireFormat: "Gemini-compatible",
		// The gateway supplies the key, so routingSuppliesKey waives the
		// user's own GEMINI_API_KEY for this mode.
		RequiresEnv: []string{"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"},
	},
	{
		Provider: "gemini", Routing: RoutingEndpoint, Status: Supported,
		WireFormat:  "Gemini-compatible",
		RequiresEnv: []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"},
	},
	{
		Provider: "gemini", Routing: RoutingShell, Status: Supported,
		WireFormat:  "Gemini-compatible",
		RequiresEnv: []string{"GOOGLE_GEMINI_BASE_URL"},
		ForwardsEnv: []string{"GEMINI_API_KEY"},
	},

	// ---- qwen --------------------------------------------------------
	{
		Provider: "qwen", Routing: RoutingDirect, Status: Supported,
		// Deliberately blanks nothing: qwen-code has no hardcoded fallback
		// endpoint, so clearing OPENAI_BASE_URL would push its OpenAI SDK to
		// api.openai.com and break users configured for another endpoint in
		// their shell or .qwen/.env.
	},
	{
		Provider: "qwen", Routing: RoutingGateway, Status: Supported,
		WireFormat:  "OpenAI-compatible",
		RequiresEnv: []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"},
		// qwen-code binds a key to a custom endpoint through a variable whose
		// NAME encodes the protocol and URL, so gateway routing works even
		// where it ignores the OPENAI_* pair.
		DynamicEnvPrefixes: []string{"QWEN_CUSTOM_API_KEY_"},
	},
	{
		Provider: "qwen", Routing: RoutingEndpoint, Status: Supported,
		WireFormat:  "OpenAI-compatible",
		RequiresEnv: []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY"},
		// A fresh install otherwise stops on its interactive sign-in picker.
		Flags: []string{"--auth-type openai"},
	},
	{
		Provider: "qwen", Routing: RoutingShell, Status: Supported,
		WireFormat:  "OpenAI-compatible",
		RequiresEnv: []string{"OPENAI_BASE_URL"},
		ForwardsEnv: []string{"OPENAI_API_KEY", "OPENAI_MODEL"},
	},

	// ---- copilot -----------------------------------------------------
	{
		Provider: "copilot", Routing: RoutingDirect, Status: Supported,
		// Blanked only when one is actually detected in the shell: merely
		// setting COPILOT_PROVIDER_BASE_URL switches Copilot to BYOK, so
		// choosing direct has to clear it or the choice would do nothing.
		// Launches with no shell endpoint are left untouched.
		BlanksEnv: []string{"COPILOT_PROVIDER_BASE_URL"},
	},
	{
		Provider: "copilot", Routing: RoutingGateway, Status: Supported,
		WireFormat: "OpenAI-compatible",
		// Same BYOK wiring as endpoint routing, pointed at the gateway.
		RequiresEnv: []string{
			"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY",
		},
		BlanksEnv: []string{"COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_WIRE_API"},
	},
	{
		Provider: "copilot", Routing: RoutingEndpoint, Status: Supported,
		WireFormat: "OpenAI-compatible",
		// Setting the base URL switches Copilot to BYOK: no GitHub model
		// routing and no GitHub login needed.
		RequiresEnv: []string{
			"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_TYPE",
			"COPILOT_PROVIDER_API_KEY", "COPILOT_MODEL",
		},
		BlanksEnv: []string{"COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_PROVIDER_WIRE_API"},
	},
	{
		Provider: "copilot", Routing: RoutingShell, Status: Supported,
		WireFormat:  "OpenAI-compatible",
		RequiresEnv: []string{"COPILOT_PROVIDER_BASE_URL"},
		// Both credential variables are passed through here, where endpoint
		// routing blanks the bearer token. Shell routing is the user's own
		// wiring, so it forwards what they exported rather than overriding it.
		ForwardsEnv: []string{
			"COPILOT_PROVIDER_TYPE", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN",
			"COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL",
		},
	},

	// ---- cursor ------------------------------------------------------
	{Provider: "cursor", Routing: RoutingDirect, Status: Supported},
	{Provider: "cursor", Routing: RoutingGateway, Status: Unsupported, Reason: reasonCursorOwnBackend},
	{Provider: "cursor", Routing: RoutingEndpoint, Status: Unsupported, Reason: reasonCursorOwnBackend},
	{Provider: "cursor", Routing: RoutingShell, Status: Unsupported, Reason: reasonCursorOwnBackend},

	// ---- kiro --------------------------------------------------------
	{Provider: "kiro", Routing: RoutingDirect, Status: Supported},
	{Provider: "kiro", Routing: RoutingGateway, Status: Unsupported, Reason: reasonKiroOwnBackend},
	{Provider: "kiro", Routing: RoutingEndpoint, Status: Unsupported, Reason: reasonKiroOwnBackend},
	{Provider: "kiro", Routing: RoutingShell, Status: Unsupported, Reason: reasonKiroOwnBackend},
}

// FindCell returns the declared cell for a harness/routing pair, or nil when
// the pair is undeclared.
func FindCell(providerKey, routing string) *RoutingCell {
	for i := range RoutingMatrix {
		if RoutingMatrix[i].Provider == providerKey && RoutingMatrix[i].Routing == routing {
			return &RoutingMatrix[i]
		}
	}
	return nil
}

// derivedSupport reports whether the code ACTUALLY supports a harness/routing
// pair, by asking the same sources the wizard and the CLI flags ask. It never
// consults RoutingMatrix — that is the whole point, since a second copied list
// is what let the gateway be offered for harnesses it did nothing for.
func derivedSupport(providerKey, routing string) bool {
	switch routing {
	case RoutingDirect:
		// Direct is the absence of redirection; every harness can do it.
		return true
	case RoutingGateway:
		return providerSupportsGateway(providerKey)
	case RoutingEndpoint:
		return providerSupportsEndpoint(providerKey)
	case RoutingShell:
		_, ok := shellEndpoints[providerKey]
		return ok
	default:
		return false
	}
}

// RenderRoutingMatrixDoc renders the matrix as the committed markdown page.
// Generated from the same declarations the tests assert against, so the
// published table cannot disagree with the code.
func RenderRoutingMatrixDoc() string {
	var b strings.Builder

	b.WriteString("# Routing matrix\n\n")
	b.WriteString("Which coding agent can use which routing mode, and what each one is verified against.\n\n")
	b.WriteString("!!! note \"Generated file\"\n")
	b.WriteString("    This page is generated from `RoutingMatrix` in `internal/vibeflowcli/routing_matrix.go`.\n")
	b.WriteString("    Regenerate it with `go test ./internal/vibeflowcli -run TestRoutingMatrixDoc -update`.\n")
	b.WriteString("    Editing it by hand will be overwritten, and CI fails when it drifts from the code.\n\n")

	b.WriteString("## Routing modes\n\n")
	b.WriteString("| Mode | What it does |\n|---|---|\n")
	b.WriteString("| `direct` | The agent talks to its own provider with your subscription, OAuth or API key. |\n")
	b.WriteString("| `gateway` | Requests route through the Axiom Studio AI Gateway for observability, cost tracking and governance. |\n")
	b.WriteString("| `endpoint` | The agent is pointed at a compatible endpoint you supply. |\n")
	b.WriteString("| `shell` | The agent uses the endpoint already exported in your shell. |\n\n")

	b.WriteString("## Support\n\n")
	b.WriteString("| Agent |")
	for _, mode := range RoutingModes {
		b.WriteString(" " + mode + " |")
	}
	b.WriteString("\n|---|")
	for range RoutingModes {
		b.WriteString("---|")
	}
	b.WriteString("\n")

	for _, provider := range matrixProviders() {
		b.WriteString("| `" + provider + "` |")
		for _, mode := range RoutingModes {
			b.WriteString(" " + cellMark(FindCell(provider, mode)) + " |")
		}
		b.WriteString("\n")
	}

	b.WriteString("\nYes means wired and covered by tests. No means the agent cannot do it and the wizard says why. ")
	b.WriteString("Gap means it should work and is not wired yet; the ticket is below.\n")

	writeMatrixNotes(&b)
	writeMatrixVerification(&b)
	return b.String()
}

// cellMark renders one support table cell.
func cellMark(c *RoutingCell) string {
	if c == nil {
		return "?"
	}
	switch c.Status {
	case Supported:
		return "yes"
	case Gap:
		return "gap (" + c.Ticket + ")"
	default:
		return "no"
	}
}

// writeMatrixNotes lists every cell that is not plain supported, with the
// reason, so the table above stays readable.
func writeMatrixNotes(b *strings.Builder) {
	var gaps, unsupported []RoutingCell
	for _, c := range RoutingMatrix {
		switch c.Status {
		case Gap:
			gaps = append(gaps, c)
		case Unsupported:
			unsupported = append(unsupported, c)
		}
	}

	b.WriteString("\n## Gaps\n\n")
	if len(gaps) == 0 {
		b.WriteString("None. Every mode an agent can support is wired.\n")
	}
	for _, c := range gaps {
		b.WriteString("- **`" + c.Provider + "` / `" + c.Routing + "`** (" + c.Ticket + ") — " + c.Reason + "\n")
	}

	b.WriteString("\n## Not supported by design\n\n")
	for _, c := range unsupported {
		b.WriteString("- **`" + c.Provider + "` / `" + c.Routing + "`** — " + c.Reason + "\n")
	}
	b.WriteString("\nThese are shown in the wizard with the reason rather than hidden, ")
	b.WriteString("so the mode is never silently missing.\n")
}

// writeMatrixVerification renders what each supported cell sets and what it
// has been live-verified against.
func writeMatrixVerification(b *strings.Builder) {
	b.WriteString("\n## What each supported mode sets\n\n")
	b.WriteString("`Blanked` variables are the leak guards: a pane inherits the tmux server's environment, ")
	b.WriteString("so a variable exported in your shell is cleared explicitly rather than merely left unset.\n\n")
	b.WriteString("**`Forwarded from your shell` variables do reach the endpoint.** They are passed through ")
	b.WriteString("as-is whenever you have them exported — this is how shell routing sends your own credential ")
	b.WriteString("to the endpoint you chose. If you do not want a credential to leave your machine, unset it ")
	b.WriteString("before launching, or use a different routing mode.\n\n")
	b.WriteString("| Agent | Mode | Wire format | Set | Blanked | Forwarded from your shell | Live-verified |\n|---|---|---|---|---|---|---|\n")

	for _, provider := range matrixProviders() {
		for _, mode := range RoutingModes {
			c := FindCell(provider, mode)
			if c == nil || c.Status != Supported {
				continue
			}
			set := append([]string{}, c.RequiresEnv...)
			for _, p := range c.DynamicEnvPrefixes {
				set = append(set, p+"*")
			}
			fmt.Fprintf(b, "| `%s` | `%s` | %s | %s | %s | %s | %s |\n",
				c.Provider, c.Routing, orDash(c.WireFormat),
				codeList(set), codeList(c.BlanksEnv), codeList(c.ForwardsEnv), verifiedNote(*c))
		}
	}
	writeMatrixCaveats(b)
}

// writeMatrixCaveats renders the per-cell safety notes under the table.
func writeMatrixCaveats(b *strings.Builder) {
	var withCaveat []RoutingCell
	for _, provider := range matrixProviders() {
		for _, mode := range RoutingModes {
			if c := FindCell(provider, mode); c != nil && c.Caveat != "" {
				withCaveat = append(withCaveat, *c)
			}
		}
	}
	if len(withCaveat) == 0 {
		return
	}
	b.WriteString("\n### Before you choose\n\n")
	for _, c := range withCaveat {
		b.WriteString("- **`" + c.Provider + "` / `" + c.Routing + "`** — " + c.Caveat + "\n")
	}
}

// verifiedNote renders the live-verification record for a cell.
func verifiedNote(c RoutingCell) string {
	if c.VerifiedOn == "" {
		return "not yet"
	}
	if c.VerifiedVersion == "" {
		return c.VerifiedOn
	}
	return c.VerifiedVersion + " on " + c.VerifiedOn
}

// matrixProviders returns the harnesses in the matrix, sorted, so the rendered
// document is stable regardless of declaration order.
func matrixProviders() []string {
	seen := make(map[string]bool)
	var out []string
	for _, c := range RoutingMatrix {
		if !seen[c.Provider] {
			seen[c.Provider] = true
			out = append(out, c.Provider)
		}
	}
	sort.Strings(out)
	return out
}

// codeList renders names as inline code, or a dash when there are none.
func codeList(names []string) string {
	if len(names) == 0 {
		return "—"
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	return strings.Join(quoted, ", ")
}

// orDash returns s, or a dash when empty.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
