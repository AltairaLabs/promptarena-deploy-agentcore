package agentcore

// Tag key constants for pack metadata applied to all created AWS resources.
const (
	TagKeyPackID  = "promptpack:pack-id"
	TagKeyVersion = "promptpack:version"
	TagKeyAgent   = "promptpack:agent"
)

// buildResourceTags merges default pack metadata tags with user-defined tags
// from the config. User-defined tags take precedence over defaults when keys
// overlap. The agentName parameter is optional; when non-empty it sets the
// promptpack:agent tag for multi-agent packs.
func buildResourceTags(
	packID, version, agentName string,
	userTags map[string]string,
) map[string]string {
	tags := make(map[string]string, len(userTags)+3) //nolint:mnd // 3 default tag keys

	// Default pack metadata tags.
	tags[TagKeyPackID] = packID
	tags[TagKeyVersion] = version
	if agentName != "" {
		tags[TagKeyAgent] = agentName
	}

	// User-defined tags override defaults.
	for k, v := range userTags {
		tags[k] = v
	}

	return tags
}

// tagsWithAgent returns a copy of the base resource tags with the
// promptpack:agent tag set to the given name. If name is empty the base
// tags are returned unmodified.
func tagsWithAgent(baseTags map[string]string, agentName string) map[string]string {
	if agentName == "" || len(baseTags) == 0 {
		return baseTags
	}
	tags := make(map[string]string, len(baseTags)+1)
	for k, v := range baseTags {
		tags[k] = v
	}
	tags[TagKeyAgent] = agentName
	return tags
}
