package agentcore

import (
	"strings"
	"testing"
)

const testRuntimeARN = "arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/mypack"

func TestConsoleURLCarriesTheRegion(t *testing.T) {
	got := consoleURL("eu-west-1")
	want := "https://eu-west-1.console.aws.amazon.com/bedrock/home?region=eu-west-1"

	if got != want {
		t.Errorf("consoleURL =\n  %q\nwant\n  %q", got, want)
	}
}

// A console URL with no region resolves against whichever region the operator
// last had selected — a wrong page rather than an absent one, and the mistake
// is invisible until they wonder why their runtime is not listed.
func TestConsoleLinksNeedARegion(t *testing.T) {
	if got := consoleURL(""); got != "" {
		t.Errorf("consoleURL = %q, want empty", got)
	}
	if links := consoleLinks(""); links != nil {
		t.Errorf("consoleLinks = %+v, want nil", links)
	}
}

func TestConsoleLinksShape(t *testing.T) {
	links := consoleLinks("us-west-2")
	if len(links) != 1 {
		t.Fatalf("consoleLinks = %+v, want exactly one link", links)
	}
	if links[0].Rel != "console" {
		t.Errorf("Rel = %q, want console", links[0].Rel)
	}
	if links[0].Label == "" {
		t.Error("a link with no label renders as a bare URL")
	}
}

// The invoke command is what a URL is for the other adapters: the thing you
// run to talk to what you just deployed. It has to carry the real ARN.
func TestInvokeHintCarriesTheARN(t *testing.T) {
	got := invokeHint(testRuntimeARN)

	if !strings.Contains(got, testRuntimeARN) {
		t.Errorf("invokeHint = %q, want it to carry the ARN", got)
	}
	if !strings.Contains(got, "invoke-agent-runtime") {
		t.Errorf("invokeHint = %q, want the documented invoke command", got)
	}
}

// Only an agent runtime is invokable. A memory store or a Cedar policy ARN
// would produce a command that cannot work.
func TestInvokeHintOnlyForRuntimeARNs(t *testing.T) {
	for _, arn := range []string{
		"",
		"not-an-arn",
		"arn:aws:bedrock:us-west-2:123456789012:something/else",
	} {
		if got := invokeHint(arn); got != "" {
			t.Errorf("invokeHint(%q) = %q, want empty", arn, got)
		}
	}
}

func TestDetailWithInvokeHint(t *testing.T) {
	t.Run("runtime gets the command appended", func(t *testing.T) {
		got := detailWithInvokeHint(ResTypeAgentRuntime, testRuntimeARN)
		if !strings.Contains(got, testRuntimeARN) {
			t.Errorf("detail lost the ARN: %q", got)
		}
		if !strings.Contains(got, "invoke-agent-runtime") {
			t.Errorf("detail = %q, want the invoke command appended", got)
		}
	})

	t.Run("other resource types are untouched", func(t *testing.T) {
		const detail = "arn:aws:bedrock-agentcore:us-west-2:123456789012:memory/m"
		if got := detailWithInvokeHint(ResTypeMemory, detail); got != detail {
			t.Errorf("detailWithInvokeHint = %q, want it unchanged", got)
		}
	})
}
