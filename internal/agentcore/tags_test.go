package agentcore

import (
	"testing"
)

func TestBuildResourceTags_DefaultsOnly(t *testing.T) {
	tags := buildResourceTags("mypack", "v1.0.0", "", nil)

	if tags[TagKeyPackID] != "mypack" {
		t.Errorf("pack-id = %q, want mypack", tags[TagKeyPackID])
	}
	if tags[TagKeyVersion] != "v1.0.0" {
		t.Errorf("version = %q, want v1.0.0", tags[TagKeyVersion])
	}
	if _, ok := tags[TagKeyAgent]; ok {
		t.Error("agent tag should not be set when agentName is empty")
	}
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tags))
	}
}

func TestBuildResourceTags_WithAgentName(t *testing.T) {
	tags := buildResourceTags("mypack", "v1.0.0", "coordinator", nil)

	if tags[TagKeyAgent] != "coordinator" {
		t.Errorf("agent = %q, want coordinator", tags[TagKeyAgent])
	}
	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d", len(tags))
	}
}

func TestBuildResourceTags_UserTagsMerged(t *testing.T) {
	userTags := map[string]string{
		"env":     "production",
		"team":    "platform",
		"project": "chatbot",
	}
	tags := buildResourceTags("mypack", "v1.0.0", "", userTags)

	if tags["env"] != "production" {
		t.Errorf("env = %q, want production", tags["env"])
	}
	if tags["team"] != "platform" {
		t.Errorf("team = %q, want platform", tags["team"])
	}
	// Default tags still present.
	if tags[TagKeyPackID] != "mypack" {
		t.Errorf("pack-id = %q, want mypack", tags[TagKeyPackID])
	}
	if len(tags) != 5 {
		t.Errorf("expected 5 tags (2 default + 3 user), got %d", len(tags))
	}
}

func TestBuildResourceTags_UserOverridesDefault(t *testing.T) {
	userTags := map[string]string{
		TagKeyPackID: "custom-id",
	}
	tags := buildResourceTags("mypack", "v1.0.0", "", userTags)

	if tags[TagKeyPackID] != "custom-id" {
		t.Errorf("pack-id = %q, want custom-id (user override)", tags[TagKeyPackID])
	}
}

func TestTagsWithAgent_AddsAgentTag(t *testing.T) {
	base := map[string]string{
		TagKeyPackID:  "mypack",
		TagKeyVersion: "v1.0.0",
	}
	tags := tagsWithAgent(base, "worker")

	if tags[TagKeyAgent] != "worker" {
		t.Errorf("agent = %q, want worker", tags[TagKeyAgent])
	}
	// Base should be unchanged.
	if _, ok := base[TagKeyAgent]; ok {
		t.Error("base tags should not be modified")
	}
	if len(tags) != 3 {
		t.Errorf("expected 3 tags, got %d", len(tags))
	}
}

func TestTagsWithAgent_EmptyName(t *testing.T) {
	base := map[string]string{
		TagKeyPackID:  "mypack",
		TagKeyVersion: "v1.0.0",
	}
	tags := tagsWithAgent(base, "")

	// Should return base unchanged.
	if len(tags) != len(base) {
		t.Errorf("expected same tags as base, got %d", len(tags))
	}
}

func TestTagsWithAgent_NilBase(t *testing.T) {
	tags := tagsWithAgent(nil, "worker")
	if tags != nil {
		t.Errorf("expected nil for nil base, got %v", tags)
	}
}
