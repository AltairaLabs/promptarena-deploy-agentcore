package agentcore

import (
	"fmt"
	"strings"

	"github.com/AltairaLabs/promptarena/deploy"
	"github.com/AltairaLabs/promptarena/deploy/adaptersdk"
)

// AgentCore has no HTTPS endpoint to hand a user: a runtime is invoked through
// the SDK with InvokeAgentRuntime, addressed by ARN. So unlike the vertex and
// foundry adapters, which link the address you POST to, the useful link here is
// the console page the runtime appears on.
//
// The link is deliberately the Bedrock console scoped to the region, not a deep
// link to the runtime itself. The console's own route to a specific AgentCore
// runtime is not something this adapter can derive from an ARN with any
// confidence, and a deep link that 404s is worse than a landing page that
// works: it looks authoritative and wastes the click. This is exactly where
// omnia's rule applies — it takes URLs from the server rather than inventing
// them, because the dashboard owns its routes.
//
// The region-scoped Bedrock console is the page this adapter's own docs already
// tell operators to open ("AWS Console under Bedrock > AgentCore"), so the link
// lands them where the instructions start, one click from the runtime, with the
// region already correct — which is the part people actually get wrong.

// consoleURL builds the Bedrock console URL for a region.
func consoleURL(region string) string {
	if region == "" {
		return ""
	}
	return fmt.Sprintf("https://%s.console.aws.amazon.com/bedrock/home?region=%s", region, region)
}

// consoleLinks wraps the console URL as resource links, or nil when the region
// is unknown. A console link without a region resolves to whichever region the
// operator last had selected, which is a wrong page rather than an absent one.
func consoleLinks(region string) []deploy.ResourceLink {
	return adaptersdk.Link("AWS console", consoleURL(region), "console")
}

// invokeHint is the command that actually talks to a deployed runtime.
//
// This is the "how do I use it" that a URL would carry for the other adapters.
// The ARN is the whole handle, so a caller who has this line needs nothing else
// from the deploy output.
func invokeHint(arn string) string {
	if !strings.HasPrefix(arn, "arn:aws:bedrock-agentcore:") {
		return ""
	}
	return fmt.Sprintf(
		"aws bedrock-agentcore invoke-agent-runtime --agent-runtime-arn %s "+
			`--runtime-session-id <session> --payload '{"prompt":"hello"}' `+
			"--content-type application/json --accept application/json /dev/stdout", arn)
}

// detailWithInvokeHint appends the invoke command to a resource's detail when
// the detail is an agent runtime ARN, and leaves everything else untouched.
func detailWithInvokeHint(resType, detail string) string {
	if resType != ResTypeAgentRuntime {
		return detail
	}
	hint := invokeHint(detail)
	if hint == "" {
		return detail
	}
	return detail + "\n" + hint
}
