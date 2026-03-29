package commanddiag

import (
	"fmt"
	"testing"
)

func TestRewriteParseErrorRewritesBareWord(t *testing.T) {
	t.Parallel()

	err := RewriteParseError("xyz\n", fmt.Errorf("bad delimiter `y'"))
	if got, want := err.Error(), "unknown command `xyz'"; got != want {
		t.Fatalf("RewriteParseError() = %q, want %q", got, want)
	}
}

func TestRewriteParseErrorKeepsSingleRuneParserDiagnostic(t *testing.T) {
	t.Parallel()

	err := RewriteParseError("Z\n", fmt.Errorf("unknown command `Z'"))
	if got, want := err.Error(), "unknown command `Z'"; got != want {
		t.Fatalf("RewriteParseError() = %q, want %q", got, want)
	}
}

func TestRewriteParseErrorKeepsStructuredSamSyntaxErrors(t *testing.T) {
	t.Parallel()

	err := RewriteParseError("x/foo/\n", fmt.Errorf("pattern expected"))
	if got, want := err.Error(), "pattern expected"; got != want {
		t.Fatalf("RewriteParseError() = %q, want %q", got, want)
	}
}

func TestPendingScriptReturnsFirstCommand(t *testing.T) {
	t.Parallel()

	if got, want := PendingScript([]rune("xyz\np\n")), "xyz\n"; got != want {
		t.Fatalf("PendingScript() = %q, want %q", got, want)
	}
}
