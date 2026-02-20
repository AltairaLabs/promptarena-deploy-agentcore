package agentcore

import (
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

// isNotFound returns true if the error is an AWS ResourceNotFoundException.
func isNotFound(err error) bool {
	var nf *types.ResourceNotFoundException
	return errors.As(err, &nf)
}

// extractResourceID attempts to extract the resource ID from an ARN.
// For example, given "arn:aws:bedrock-agentcore:us-west-2:123:runtime/abc123"
// and prefix "runtime", it returns "abc123".
func extractResourceID(arn, prefix string) string {
	search := prefix + "/"
	for i := 0; i <= len(arn)-len(search); i++ {
		if arn[i:i+len(search)] == search {
			return arn[i+len(search):]
		}
	}
	return ""
}
