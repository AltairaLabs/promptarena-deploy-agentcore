package agentcore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

func TestExtractResourceID(t *testing.T) {
	tests := []struct {
		name   string
		arn    string
		prefix string
		want   string
	}{
		{
			name:   "valid agent-runtime ARN",
			arn:    "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/abc123",
			prefix: "agent-runtime",
			want:   "abc123",
		},
		{
			name:   "valid gateway ARN",
			arn:    "arn:aws:bedrock:us-west-2:123456789012:gateway/gw-99",
			prefix: "gateway",
			want:   "gw-99",
		},
		{
			name:   "no match",
			arn:    "arn:aws:bedrock:us-west-2:123456789012:other/foo",
			prefix: "agent-runtime",
			want:   "",
		},
		{
			name:   "empty ARN",
			arn:    "",
			prefix: "agent-runtime",
			want:   "",
		},
		{
			name:   "prefix at start",
			arn:    "agent-runtime/abc123",
			prefix: "agent-runtime",
			want:   "abc123",
		},
		{
			name:   "multiple slashes in ID",
			arn:    "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/abc/def",
			prefix: "agent-runtime",
			want:   "abc/def",
		},
		{
			name:   "empty prefix matches first slash",
			arn:    "arn:aws:bedrock:us-west-2:123456789012:agent-runtime/abc",
			prefix: "",
			want:   "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResourceID(tt.arn, tt.prefix)
			if got != tt.want {
				t.Errorf("extractResourceID(%q, %q) = %q, want %q", tt.arn, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	t.Run("ResourceNotFoundException", func(t *testing.T) {
		err := &types.ResourceNotFoundException{Message: strPtr("not found")}
		if !isNotFound(err) {
			t.Error("expected true for ResourceNotFoundException")
		}
	})

	t.Run("wrapped ResourceNotFoundException", func(t *testing.T) {
		inner := &types.ResourceNotFoundException{Message: strPtr("gone")}
		err := fmt.Errorf("wrap: %w", inner)
		if !isNotFound(err) {
			t.Error("expected true for wrapped ResourceNotFoundException")
		}
	})

	t.Run("other error", func(t *testing.T) {
		err := errors.New("something else")
		if isNotFound(err) {
			t.Error("expected false for generic error")
		}
	})
}

func strPtr(s string) *string { return &s }
