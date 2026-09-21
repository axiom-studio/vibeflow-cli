package vibeflowcli

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// updateRoutingDoc regenerates the committed routing-matrix page instead of
// asserting against it:
//
//	go test ./internal/vibeflowcli -run TestRoutingMatrixDoc -update
var updateRoutingDoc = flag.Bool("update", false, "regenerate docs/VibeFlow-CLI/docs/routing-matrix.md")

// routingMatrixDocPath is the generated page, relative to this package.
const routingMatrixDocPath = "../../docs/VibeFlow-CLI/docs/routing-matrix.md"

// Fixed inputs for driving the real builders. The key is deliberately
// recognisable so an assertion can prove it never reaches a command line.
const (
	testGatewayServer = "https://server.example"
	testGatewayToken  = "gateway-token-value"
	testEndpointURL   = "https://endpoint.example/v1"
	testEndpointModel = "test-model"
	testVendor        = "acme"
	testVendorKey     = "vendor-key-value"
	// forwardedShellValue stands in for whatever the user has exported. It is
	// recognisable so an assertion can tell a forwarded value apart from one
	// the builder chose.
	forwardedShellValue = "shell-exported-value"
)

// TestRoutingMatrixIsExhaustive fails when a harness in the live provider
// registry has no declaration for some routing mode.
//
// This is the guard that makes the matrix self-maintaining: an eighth harness
// cannot ship until someone has decided, and tested, what all four of its
// routing modes do.
func TestRoutingMatrixIsExhaustive(t *testing.T) {
	registry := NewProviderRegistry(DefaultConfig())
	for _, key := range registry.Keys() {
		for _, mode := range RoutingModes {
			if FindCell(key, mode) == nil {
				t.Errorf("harness %q × routing %q missing from RoutingMatrix — "+
					"add the cell (and its test expectations) before shipping the harness", key, mode)
			}
		}
	}
}

// TestRoutingMatrixDeclaresNothingUnknown is the reverse guard: a cell for a
// harness that no longer exists would quietly keep passing every other test.
func TestRoutingMatrixDeclaresNothingUnknown(t *testing.T) {
	registry := NewProviderRegistry(DefaultConfig())
	known := make(map[string]bool)
	for _, key := range registry.Keys() {
		known[key] = true
	}
	validMode := make(map[string]bool)
	for _, mode := range RoutingModes {
		validMode[mode] = true
	}

	seen := make(map[string]bool)
	for _, c := range RoutingMatrix {
		if !known[c.Provider] {
			t.Errorf("RoutingMatrix declares unknown harness %q — remove the cell or restore the provider", c.Provider)
		}
		if !validMode[c.Routing] {
			t.Errorf("harness %q declares unknown routing mode %q", c.Provider, c.Routing)
		}
		cellKey := c.Provider + "/" + c.Routing
		if seen[cellKey] {
			t.Errorf("duplicate cell for %s — the first declaration would always win", cellKey)
		}
		seen[cellKey] = true
	}
}

// TestRoutingMatrix drives every declared cell through the real builders and
// asserts the declaration matches what the code actually does.
func TestRoutingMatrix(t *testing.T) {
	for _, cell := range RoutingMatrix {
		c := cell
		t.Run(c.Provider+"/"+c.Routing, func(t *testing.T) {
			isolateRoutingEnv(t)

			// The declared status must match what the code supports. A cell
			// cannot claim a mode works when no builder wires it, and a Gap
			// cannot survive the fix that closes it.
			supported := derivedSupport(c.Provider, c.Routing)
			if (c.Status == Supported) != supported {
				t.Fatalf("cell says %s but the builders say supported=%v — "+
					"update the cell (a closed gap must become Supported)", c.Status, supported)
			}

			switch c.Status {
			case Supported:
				assertSupportedCell(t, c)
			case Gap:
				if c.Ticket == "" {
					t.Error("a Gap cell must carry a Ticket, otherwise nothing tracks closing it")
				}
				assertUnavailableCell(t, c)
			case Unsupported:
				if c.Reason == "" {
					t.Error("an Unsupported cell must carry a Reason — it is shown to the user in the wizard")
				}
				assertUnavailableCell(t, c)
			}
		})
	}
}

// assertSupportedCell checks a wired cell sets what it declares, blanks what
// it declares, and declares everything it sets.
func assertSupportedCell(t *testing.T, c RoutingCell) {
	t.Helper()
	env, flags := buildForCell(c)

	// Direct routing is the absence of redirection, so an empty env is
	// correct there. Every other mode must actually wire something.
	if c.Routing != RoutingDirect && len(env) == 0 {
		t.Fatalf("%s/%s claims Supported but the builder produced no session env", c.Provider, c.Routing)
	}

	for _, name := range c.RequiresEnv {
		value, ok := env[name]
		if !ok {
			t.Errorf("%s must be set for %s/%s but the builder did not set it", name, c.Provider, c.Routing)
			continue
		}
		if value == "" {
			t.Errorf("%s must be non-empty for %s/%s", name, c.Provider, c.Routing)
		}
	}

	// The leak guards. A variable left merely unset is inherited from the
	// tmux server's environment; only an explicit empty value masks it.
	for _, name := range c.BlanksEnv {
		value, ok := env[name]
		if !ok {
			t.Errorf("%s must be blanked for %s/%s, but the builder did not set it at all — "+
				"a value exported in the user's shell would be inherited", name, c.Provider, c.Routing)
			continue
		}
		if value != "" {
			t.Errorf("%s must be blanked for %s/%s, got %q", name, c.Provider, c.Routing, value)
		}
	}

	// Everything the builder emits must be declared. This is what turns a new
	// undeclared variable — the shape a silent credential leak takes — into a
	// failing test rather than an unnoticed change.
	declared := make(map[string]bool)
	for _, name := range c.RequiresEnv {
		declared[name] = true
	}
	for _, name := range c.BlanksEnv {
		declared[name] = true
	}
	for _, name := range c.ForwardsEnv {
		declared[name] = true
	}
	for name := range env {
		if declared[name] || hasDeclaredPrefix(name, c.DynamicEnvPrefixes) {
			continue
		}
		t.Errorf("%s/%s sets undeclared variable %s — add it to RequiresEnv, BlanksEnv or "+
			"ForwardsEnv so its behaviour is pinned", c.Provider, c.Routing, name)
	}

	// A declared pass-through must really carry the shell's value, not a
	// value the builder chose. This is what makes the credential flow in the
	// generated page verifiable rather than merely asserted.
	for _, name := range c.ForwardsEnv {
		if got := env[name]; got != "" && got != forwardedShellValue {
			t.Errorf("%s/%s declares %s as forwarded from the shell, but the builder set its own "+
				"value %q — move it to RequiresEnv", c.Provider, c.Routing, name, got)
		}
	}

	for _, fragment := range c.Flags {
		if !strings.Contains(flags, fragment) {
			t.Errorf("%s/%s must append flag fragment %q, got %q", c.Provider, c.Routing, fragment, flags)
		}
	}

	// Credentials travel in the environment, never on the command line: argv
	// is world-readable and is copied into the spawn log.
	for _, secret := range []string{testVendorKey, testGatewayToken} {
		if strings.Contains(flags, secret) {
			t.Errorf("%s/%s put a credential on the command line: %q", c.Provider, c.Routing, flags)
		}
	}
}

// assertUnavailableCell checks a Gap or Unsupported cell wires nothing and is
// not offered to the user as a working choice.
func assertUnavailableCell(t *testing.T, c RoutingCell) {
	t.Helper()
	env, _ := buildForCell(c)
	if len(env) != 0 {
		t.Errorf("%s/%s is %s but the builder produced env %v — "+
			"a mode that sets something is not unavailable", c.Provider, c.Routing, c.Status, env)
	}

	// The failure a user actually sees is the wizard offering a route that
	// does nothing, so assert against the same predicates the wizard uses.
	switch c.Routing {
	case RoutingGateway:
		if providerSupportsGateway(c.Provider) {
			t.Errorf("wizard would offer the gateway for %s as a working choice", c.Provider)
		}
		if gatewayUnsupportedReason(c.Provider, "") == "" {
			t.Errorf("wizard would show no reason on the dimmed gateway row for %s", c.Provider)
		}
	case RoutingEndpoint:
		if _, ok := EndpointAPIFormat(c.Provider); ok {
			t.Errorf("wizard would offer a compatible endpoint for %s", c.Provider)
		}
	case RoutingShell:
		if _, ok := shellEndpoints[c.Provider]; ok {
			t.Errorf("wizard would offer a detected shell endpoint for %s", c.Provider)
		}
	}
}

// buildForCell runs the real builder for a cell and returns the session env
// plus the launch flags the mode appends.
func buildForCell(c RoutingCell) (map[string]string, string) {
	switch c.Routing {
	case RoutingDirect:
		// Direct explicitly clears both a gateway and a shell endpoint left
		// in the environment, so the harness really falls back to its own
		// login instead of quietly keeping the redirect.
		env := ClearLLMGatewayEnv(c.Provider)
		for name, value := range ClearShellEndpointEnv(c.Provider) {
			env[name] = value
		}
		return env, ""
	case RoutingGateway:
		return BuildLLMGatewayEnv(c.Provider, testGatewayServer, testGatewayToken), ""
	case RoutingEndpoint:
		cfg := DefaultConfig()
		return BuildEndpointEnv(c.Provider, cfg, testVendor, testEndpointURL, testEndpointModel),
			AppendEndpointFlags("", c.Provider, testEndpointURL)
	case RoutingShell:
		return BuildShellEndpointEnv(c.Provider, testEndpointURL),
			AppendShellEndpointFlags("", c.Provider, testEndpointURL)
	default:
		return map[string]string{}, ""
	}
}

// hasDeclaredPrefix reports whether name is covered by a declared dynamic
// prefix (the qwen custom-API-key binding encodes the URL into the name).
func hasDeclaredPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// isolateRoutingEnv blanks every variable the builders read from the ambient
// environment, so the assertions describe the code rather than whatever the
// developer or CI runner happens to export.
func isolateRoutingEnv(t *testing.T) {
	t.Helper()
	// Every variable shell routing can forward is EXPORTED, not blanked.
	// Blanking them made the "every emitted variable is declared" assertion
	// vacuous for the shell cells: BuildShellEndpointEnv only emits a related
	// variable when it is non-empty, so with them all empty the guard had
	// nothing to bind to and a newly forwarded credential went unnoticed.
	for _, se := range shellEndpoints {
		t.Setenv(se.urlVar, testEndpointURL)
		for _, related := range se.related {
			t.Setenv(related, forwardedShellValue)
		}
	}
	// The vendor's key is present so keyed cells are deterministic; the
	// assertions then prove it reaches the env and never the command line.
	t.Setenv(OpenAICompatKeyEnvName(testVendor), testVendorKey)
}

// TestRoutingMatrixDoc keeps the committed page identical to what the matrix
// renders, so the published table cannot drift from the code.
func TestRoutingMatrixDoc(t *testing.T) {
	got := RenderRoutingMatrixDoc()
	path := filepath.Clean(routingMatrixDocPath)

	if *updateRoutingDoc {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("regenerated %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with: go test ./internal/vibeflowcli -run TestRoutingMatrixDoc -update)", path, err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date — regenerate it with:\n"+
			"\tgo test ./internal/vibeflowcli -run TestRoutingMatrixDoc -update", path)
	}
}

// TestRoutingMatrixVerificationRecords keeps every live-verification record
// self-describing: a date and the agent version it was run against travel
// together, and the date is a real date.
//
// Without this, a cell could carry a bare date that silently ages into a
// claim about whatever version the agent ships today — the failure mode of a
// verified column that nobody re-runs.
func TestRoutingMatrixVerificationRecords(t *testing.T) {
	for _, c := range RoutingMatrix {
		name := c.Provider + "/" + c.Routing
		switch {
		case c.VerifiedOn == "" && c.VerifiedVersion == "":
			continue // not yet live-verified, which is an honest state
		case c.VerifiedOn == "":
			t.Errorf("%s records VerifiedVersion %q with no VerifiedOn — a version without a date "+
				"cannot be judged for staleness", name, c.VerifiedVersion)
		case c.VerifiedVersion == "":
			t.Errorf("%s records VerifiedOn %q with no VerifiedVersion — a bare date ages into a claim "+
				"about whichever version ships today", name, c.VerifiedOn)
		default:
			if _, err := time.Parse("2006-01-02", c.VerifiedOn); err != nil {
				t.Errorf("%s has VerifiedOn %q, want YYYY-MM-DD: %v", name, c.VerifiedOn, err)
			}
		}
		// Only a cell that actually works can have been verified against a
		// live endpoint; a gap or an unsupported mode has nothing to run.
		if c.Status != Supported && (c.VerifiedOn != "" || c.VerifiedVersion != "") {
			t.Errorf("%s is %s but carries a live-verification record", name, c.Status)
		}
	}
}

// TestRoutingMatrixCoversEveryProviderOnce is a readability guard: the
// rendered document lists each harness once, in a stable order.
func TestRoutingMatrixCoversEveryProviderOnce(t *testing.T) {
	providers := matrixProviders()
	if !sort.StringsAreSorted(providers) {
		t.Errorf("matrixProviders must be sorted for a stable document, got %v", providers)
	}
	if len(providers)*len(RoutingModes) != len(RoutingMatrix) {
		t.Errorf("matrix has %d cells but %d harnesses × %d modes = %d — every harness needs every mode",
			len(RoutingMatrix), len(providers), len(RoutingModes), len(providers)*len(RoutingModes))
	}
}
