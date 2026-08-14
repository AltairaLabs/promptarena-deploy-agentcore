package agentcore

import (
	"fmt"
	"sort"
	"strings"
)

// Provider binding roles. A binding declares which capability the bound
// provider serves in the deployed runtime.
const (
	RoleLLM       = "llm"
	RoleEmbedding = "embedding"
	RoleTTS       = "tts"
	RoleSTT       = "stt"
	RoleImage     = "image"
	RoleInference = "inference"
)

// validRoles lists the accepted binding roles.
var validRoles = map[string]bool{
	RoleLLM:       true,
	RoleEmbedding: true,
	RoleTTS:       true,
	RoleSTT:       true,
	RoleImage:     true,
	RoleInference: true,
}

// defaultBindingName is the binding name that marks the runtime's primary
// provider. A binding with this name is always the primary.
const defaultBindingName = "default"

// ProviderBinding declares one provider the deployed runtime should use.
//
// A binding resolves its type and model either by naming a provider from the
// arena config (ArenaProvider, preserving "deploy what you tested") or by
// declaring them inline (keeping the deploy config self-contained). When both
// are given, the inline fields win field-by-field.
type ProviderBinding struct {
	// Name is the logical binding name, unique within the deploy config.
	// The binding named "default" is the runtime's primary provider.
	Name string `json:"name"`

	// Role is the capability this provider serves. Defaults to "llm".
	Role string `json:"role,omitempty"`

	// ArenaProvider names a provider from the arena config to inherit
	// type and model from.
	ArenaProvider string `json:"arena_provider,omitempty"`

	// Type and Model declare the provider inline, overriding anything
	// inherited via ArenaProvider.
	Type  string `json:"type,omitempty"`
	Model string `json:"model,omitempty"`
}

// ResolvedProvider is a fully-resolved binding, ready to be serialized into
// the PROMPTPACK_PROVIDERS env var the runtime reads.
type ResolvedProvider struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Type    string `json:"type"`
	Model   string `json:"model,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// role returns the binding's role, defaulting to "llm".
func (b *ProviderBinding) role() string {
	if b.Role == "" {
		return RoleLLM
	}
	return b.Role
}

// resolveProviderBindings turns the deploy config's provider bindings into
// fully-resolved providers, marking exactly one as primary where possible.
//
// When no bindings are declared it falls back to the legacy behavior of
// deriving a single provider from the arena config, and returns a deprecation
// warning saying which provider was chosen.
//
// Returned warnings are advisory; hard errors come from validateProviderBindings.
func resolveProviderBindings(cfg *Config) (resolved []ResolvedProvider, warnings []string) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.Providers) == 0 {
		return resolveLegacyProvider(cfg.ArenaConfig)
	}

	resolved = make([]ResolvedProvider, 0, len(cfg.Providers))
	for i := range cfg.Providers {
		b := &cfg.Providers[i]
		rp := ResolvedProvider{Name: b.Name, Role: b.role(), Type: b.Type, Model: b.Model}
		if p := cfg.ArenaConfig.providerByName(b.ArenaProvider); p != nil {
			if rp.Type == "" {
				rp.Type = p.Type
			}
			if rp.Model == "" {
				rp.Model = p.Model
			}
		}
		resolved = append(resolved, rp)
	}

	return resolved, markPrimary(resolved)
}

// markPrimary flags exactly one resolved provider as the runtime's primary.
// A binding named "default" always wins; otherwise the first llm-role binding
// in declaration order is used and a warning naming it is returned.
func markPrimary(resolved []ResolvedProvider) []string {
	for i := range resolved {
		if resolved[i].Name == defaultBindingName {
			resolved[i].Primary = true
			return nil
		}
	}
	for i := range resolved {
		if resolved[i].Role == RoleLLM {
			resolved[i].Primary = true
			return []string{fmt.Sprintf(
				"no provider binding named %q; treating %q as the primary provider — "+
					"name a binding %q to make this explicit",
				defaultBindingName, resolved[i].Name, defaultBindingName)}
		}
	}
	return []string{fmt.Sprintf(
		"no provider binding named %q and no binding with role %q; "+
			"the runtime will have no primary LLM provider",
		defaultBindingName, RoleLLM)}
}

// resolveLegacyProvider derives a single provider from the arena config, the
// pre-bindings behavior. Selection is deterministic (lowest provider key).
func resolveLegacyProvider(arena *ArenaConfig) (resolved []ResolvedProvider, warnings []string) {
	name, p := arena.firstProvider()
	if p == nil {
		return nil, nil
	}
	warn := fmt.Sprintf(
		"deploy config declares no `providers` bindings; falling back to arena provider %q "+
			"(type=%s model=%s). This fallback is deprecated — declare an explicit "+
			"`providers` binding to control what is deployed",
		name, p.Type, p.Model)
	return []ResolvedProvider{{
		Name:    defaultBindingName,
		Role:    RoleLLM,
		Type:    p.Type,
		Model:   p.Model,
		Primary: true,
	}}, []string{warn}
}

// validateProviderBindings checks the provider bindings and returns any
// validation errors.
func validateProviderBindings(cfg *Config) []string {
	if cfg == nil || len(cfg.Providers) == 0 {
		return nil
	}
	var errs []string
	seen := make(map[string]bool, len(cfg.Providers))

	for i := range cfg.Providers {
		b := &cfg.Providers[i]
		label := fmt.Sprintf("providers[%d]", i)

		if b.Name == "" {
			errs = append(errs, label+": name is required")
		} else if seen[b.Name] {
			errs = append(errs, fmt.Sprintf("%s: duplicate binding name %q", label, b.Name))
		} else {
			seen[b.Name] = true
			label = fmt.Sprintf("providers[%q]", b.Name)
		}

		if b.Role != "" && !validRoles[b.Role] {
			errs = append(errs, fmt.Sprintf("%s: role %q must be one of %s",
				label, b.Role, strings.Join(sortedRoles(), ", ")))
		}

		errs = append(errs, validateBindingSource(cfg.ArenaConfig, b, label)...)
	}
	return errs
}

// validateBindingSource checks that a binding can actually resolve a provider
// type, either from a named arena provider or from inline fields.
func validateBindingSource(arena *ArenaConfig, b *ProviderBinding, label string) []string {
	if b.ArenaProvider != "" {
		// The reference can only be checked when an arena config is present.
		// ValidateConfig legitimately has none — deploy.ValidateRequest carries
		// only the deploy config — so an absent arena config means "unknown",
		// not "invalid". Plan checks it for real once the arena config is
		// parsed.
		if arena == nil {
			return nil
		}
		if arena.providerByName(b.ArenaProvider) == nil {
			return []string{fmt.Sprintf(
				"%s: arena_provider %q not found in the arena config", label, b.ArenaProvider)}
		}
		return nil
	}
	if b.Type == "" {
		return []string{fmt.Sprintf(
			"%s: type is required when arena_provider is not set", label)}
	}
	return nil
}

// sortedRoles returns the valid role names in a stable order, for messages.
func sortedRoles() []string {
	roles := make([]string, 0, len(validRoles))
	for r := range validRoles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return roles
}
