package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/AltairaLabs/promptarena/v2/deploy"
	"github.com/AltairaLabs/promptarena/v2/deploy/adaptersdk"
)

// Every UpdateAgentRuntime makes a new immutable version, and AgentCore's
// DEFAULT endpoint always serves the latest one. A caller on DEFAULT therefore
// gets each new version the moment it exists, which leaves no way to stage a
// version without serving it. Named endpoints pin a version, so the adapter
// manages two of them: live, which callers invoke, and candidate, which a
// staged version waits on until it is promoted.

// Rollout stages (rollout_stage).
const (
	// StageLive points the live endpoint at the version the deploy makes.
	StageLive = "live"
	// StageCandidate points the candidate endpoint at the version the deploy
	// makes and leaves live where it was.
	StageCandidate = "candidate"
	// StagePromote makes no version and points live at the candidate's.
	StagePromote = "promote"
)

// Default endpoint names, and the one AgentCore reserves.
const (
	defaultLiveEndpoint      = "live"
	defaultCandidateEndpoint = "candidate"
	reservedEndpointName     = "DEFAULT"
)

// endpointNameRE is AgentCore's pattern for a runtime endpoint name.
var endpointNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,47}$`)

// stage returns the configured rollout stage, defaulting to live.
func (c *Config) stage() string {
	if c.RolloutStage == "" {
		return StageLive
	}
	return c.RolloutStage
}

// liveEndpoint returns the name of the endpoint callers invoke.
func (c *Config) liveEndpoint() string {
	if c.LiveEndpoint == "" {
		return defaultLiveEndpoint
	}
	return c.LiveEndpoint
}

// candidateEndpoint returns the name of the endpoint a staged version is on.
func (c *Config) candidateEndpoint() string {
	if c.CandidateEndpoint == "" {
		return defaultCandidateEndpoint
	}
	return c.CandidateEndpoint
}

// targetEndpoint is the endpoint a deploy that makes a version points at it.
func (c *Config) targetEndpoint() string {
	if c.stage() == StageCandidate {
		return c.candidateEndpoint()
	}
	return c.liveEndpoint()
}

// validateRollout checks rollout_stage, live_endpoint and candidate_endpoint.
func validateRollout(c *Config) []string {
	var errs []string
	switch c.RolloutStage {
	case "", StageLive, StageCandidate, StagePromote:
	default:
		errs = append(errs, fmt.Sprintf("rollout_stage %q must be %q, %q or %q",
			c.RolloutStage, StageLive, StageCandidate, StagePromote))
	}
	for _, f := range []struct{ key, val string }{
		{"live_endpoint", c.LiveEndpoint}, {"candidate_endpoint", c.CandidateEndpoint},
	} {
		switch {
		case f.val == "":
		case !endpointNameRE.MatchString(f.val):
			errs = append(errs, fmt.Sprintf(
				"%s %q must start with a letter and hold only letters, digits and _ (at most 48)", f.key, f.val))
		case strings.EqualFold(f.val, reservedEndpointName):
			errs = append(errs, fmt.Sprintf("%s %q is reserved by AgentCore", f.key, f.val))
		}
	}
	if strings.EqualFold(c.liveEndpoint(), c.candidateEndpoint()) {
		errs = append(errs, fmt.Sprintf("live_endpoint and candidate_endpoint must differ (both %q)", c.liveEndpoint()))
	}
	return errs
}

// endpointResourceName names a runtime_endpoint entry: <runtime>/<endpoint>.
func endpointResourceName(runtime, endpoint string) string {
	return runtime + "/" + endpoint
}

// packVersionKey is the key a version's description carries the pack version
// under, as the last token: "<any existing text> pack_version=<version>".
const packVersionKey = "pack_version"

// maxRuntimeDescriptionLen is the longest description AgentCore accepts.
const maxRuntimeDescriptionLen = 1200

// packVersionTokenRE matches a pack_version token anywhere in a description.
var packVersionTokenRE = regexp.MustCompile(`(^|\s)` + packVersionKey + `=\S*`)

// versionDescription returns the description to give a new runtime version:
// the existing text, with any earlier pack_version token removed, followed by
// "pack_version=<packVersion>". Whitespace in the version becomes "_" so the
// token stays one word. Existing text is shortened, never the token, when the
// whole would exceed AgentCore's limit. An empty pack version leaves the text
// as it is.
func versionDescription(existing, packVersion string) string {
	base := strings.TrimSpace(packVersionTokenRE.ReplaceAllString(existing, ""))
	if packVersion == "" {
		return base
	}
	token := packVersionKey + "=" + strings.Join(strings.Fields(packVersion), "_")
	if base == "" {
		return token
	}
	if room := maxRuntimeDescriptionLen - len(token) - 1; len(base) > room {
		base = strings.TrimSpace(base[:max(room, 0)])
	}
	if base == "" {
		return token
	}
	return base + " " + token
}

// evalServiceNames is the trace service name an online evaluation config
// reads. AgentCore names a runtime's traces <runtime>.<endpoint>, and the
// config accepts exactly one name, so it reads the live endpoint's: the
// traffic callers are told to send. Traffic on DEFAULT or the candidate
// endpoint is not evaluated.
func evalServiceNames(cfg *Config) []string {
	return []string{cfg.ResourceTags[TagKeyPackID] + "." + cfg.liveEndpoint()}
}

// ---------- apply ----------

// applyEndpointsPhase points the endpoint this deploy targets (live, or
// candidate when staging) at the version each runtime now has, and carries
// forward the runtime's other endpoints. It runs after every update the apply
// makes to a runtime, so the version it reads is the last one.
func applyEndpointsPhase(
	ctx context.Context, ac *applyContext, resources []ResourceState,
) ([]ResourceState, error, error) {
	target := ac.cfg.targetEndpoint()
	var entries []ResourceState
	var applyErr error
	for i := range resources {
		rt := &resources[i]
		if rt.Type != ResTypeAgentRuntime || rt.ARN == "" || rt.Status == ResStatusFailed {
			continue
		}
		got, err, cbErr := pointRuntime(ctx, ac, rt, target)
		entries = append(entries, got...)
		if cbErr != nil {
			return append(resources, entries...), applyErr, cbErr
		}
		applyErr = combineErrors(applyErr, err)
	}
	return append(resources, entries...), applyErr, nil
}

// pointRuntime points one runtime's target endpoint at its latest version and
// returns the runtime's endpoint entries.
func pointRuntime(
	ctx context.Context, ac *applyContext, rt *ResourceState, target string,
) ([]ResourceState, error, error) {
	name := endpointResourceName(rt.Name, target)
	prior, hadPrior := ac.priorMap[resourceKey(ResTypeRuntimeEndpoint, name)]
	others := carriedEndpoints(ctx, ac.client, ac.priorMap, rt.Name, target)
	keepPrior := func() []ResourceState {
		if hadPrior {
			return append(others, prior)
		}
		return others
	}

	version, err := ac.client.RuntimeVersion(ctx, rt.ARN)
	if err == nil && version == "" {
		err = errors.New("runtime reports no version")
	}
	if err != nil {
		deployErr := newDeployError("update", ResTypeRuntimeEndpoint, name, err)
		_ = ac.reporter.Error(deployErr)
		return keepPrior(), deployErr, nil
	}
	setMetadata(rt, metaLatestVersion, version)
	setMetadata(rt, metaPackVersion, ac.pack.Version)

	msg := fmt.Sprintf("Pointing endpoint %s of %s at version %s", target, rt.Name, version)
	if cbErr := ac.reporter.Progress(msg, float64(stepA2A)*progressStepSize); cbErr != nil {
		return keepPrior(), nil, cbErr
	}
	arn, err := ac.client.PointRuntimeEndpoint(ctx, rt.ARN, target, version)
	if err != nil {
		deployErr := newDeployError("update", ResTypeRuntimeEndpoint, name, err)
		_ = ac.reporter.Error(deployErr)
		return keepPrior(), deployErr, nil
	}

	action, status := deploy.ActionCreate, ResStatusCreated
	if hadPrior {
		action, status = deploy.ActionUpdate, ResStatusUpdated
	}
	if err := ac.reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeRuntimeEndpoint, Name: name, Action: action, Status: status,
		Detail: endpointDetail(target, version),
	}); err != nil {
		return keepPrior(), nil, err
	}
	return append(others, endpointState(rt.Name, rt.ARN, target, arn, version, status)), nil, nil
}

// carriedEndpoints returns the prior endpoint entries of runtime other than
// skip, each with the version the platform now reports. An endpoint the
// platform no longer has is dropped; one it cannot be asked about is kept as
// recorded.
func carriedEndpoints(
	ctx context.Context, client awsClient, priorMap map[string]ResourceState, runtime, skip string,
) []ResourceState {
	var out []ResourceState
	for _, r := range sortedEndpoints(priorMap, runtime) {
		ep := r.Metadata[metaEndpoint]
		if ep == skip {
			continue
		}
		version, err := client.RuntimeEndpointVersion(ctx, r.Metadata[metaRuntimeARN], ep)
		if err == nil && version == "" {
			continue
		}
		if err == nil {
			r.Metadata = cloneMetadata(r.Metadata)
			r.Metadata[metaVersion] = version
		}
		out = append(out, r)
	}
	return out
}

// sortedEndpoints returns the endpoint entries recorded for runtime, by name.
func sortedEndpoints(priorMap map[string]ResourceState, runtime string) []ResourceState {
	var out []ResourceState
	for _, r := range priorMap {
		if r.Type == ResTypeRuntimeEndpoint && strings.HasPrefix(r.Name, runtime+"/") && r.Metadata[metaEndpoint] != "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// endpointState is the state entry for one runtime endpoint.
func endpointState(runtime, runtimeARN, endpoint, arn, version, status string) ResourceState {
	return ResourceState{
		Type: ResTypeRuntimeEndpoint, Name: endpointResourceName(runtime, endpoint),
		ARN: arn, Status: status,
		Metadata: map[string]string{metaRuntimeARN: runtimeARN, metaEndpoint: endpoint, metaVersion: version},
	}
}

// endpointDetail describes what an endpoint serves and how to call it.
func endpointDetail(endpoint, version string) string {
	return fmt.Sprintf("%s serves version %s (invoke with qualifier=%s)", endpoint, version, endpoint)
}

// setMetadata sets one metadata key on a resource.
func setMetadata(r *ResourceState, key, val string) {
	if val == "" {
		return
	}
	r.Metadata = cloneMetadata(r.Metadata)
	r.Metadata[key] = val
}

// cloneMetadata copies a metadata map so an entry shared with the prior
// state is never written through.
func cloneMetadata(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---------- promote ----------

// applyPromote points each deployed runtime's live endpoint at the version
// its candidate endpoint serves, read from the platform. It makes no version
// and touches nothing else, so it needs only the prior state. Promoting what
// live already serves changes nothing.
func (p *Provider) applyPromote(
	ctx context.Context, req *deploy.PlanRequest, cfg *Config, callback deploy.ApplyCallback,
) (string, error) {
	prior, err := parseAdapterState(req.PriorState)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to parse prior state: %w", err)
	}
	runtimes := deployedRuntimes(prior)
	if len(runtimes) == 0 {
		return "", fmt.Errorf("agentcore: rollout_stage %q needs a deployed runtime; "+
			"deploy one with rollout_stage %q first", StagePromote, StageCandidate)
	}
	client, err := p.awsClientFunc(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("agentcore: failed to create AWS client: %w", err)
	}
	reporter := newReporter(callback)
	priorMap := parsePriorState(req.PriorState)

	var promoteErr error
	resources := prior.Resources
	for _, rt := range runtimes {
		entry, rtErr := promoteRuntime(ctx, client, reporter, cfg, rt, priorMap)
		if rtErr != nil {
			promoteErr = combineErrors(promoteErr, rtErr)
			continue
		}
		resources = upsertResource(resources, entry)
	}
	state := *prior
	state.Resources = resources
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return "", errors.Join(promoteErr, fmt.Errorf("agentcore: failed to marshal state: %w", err))
	}
	return string(stateJSON), promoteErr
}

// promoteRuntime points one runtime's live endpoint at its candidate's version.
func promoteRuntime(
	ctx context.Context, client awsClient, reporter *adaptersdk.ProgressReporter,
	cfg *Config, rt ResourceState, priorMap map[string]ResourceState,
) (ResourceState, error) {
	live, cand := cfg.liveEndpoint(), cfg.candidateEndpoint()
	name := endpointResourceName(rt.Name, live)
	fail := func(err error) (ResourceState, error) {
		deployErr := newDeployError("update", ResTypeRuntimeEndpoint, name, err)
		_ = reporter.Error(deployErr)
		return ResourceState{}, deployErr
	}
	version, err := client.RuntimeEndpointVersion(ctx, rt.ARN, cand)
	if err != nil {
		return fail(fmt.Errorf("read endpoint %s: %w", cand, err))
	}
	if version == "" {
		return fail(fmt.Errorf("runtime %s has no %s endpoint to promote; "+
			"deploy with rollout_stage %q first", rt.Name, cand, StageCandidate))
	}
	current, err := client.RuntimeEndpointVersion(ctx, rt.ARN, live)
	if err != nil {
		return fail(fmt.Errorf("read endpoint %s: %w", live, err))
	}
	msg := fmt.Sprintf("Promoting %s version %s to %s", rt.Name, version, live)
	if cbErr := reporter.Progress(msg, 0); cbErr != nil {
		return ResourceState{}, cbErr
	}
	arn, err := client.PointRuntimeEndpoint(ctx, rt.ARN, live, version)
	if err != nil {
		return fail(err)
	}
	action, status := promoteOutcome(priorMap[resourceKey(ResTypeRuntimeEndpoint, name)], current, version)
	if err := reporter.Resource(&deploy.ResourceResult{
		Type: ResTypeRuntimeEndpoint, Name: name, Action: action, Status: status,
		Detail: endpointDetail(live, version),
	}); err != nil {
		return ResourceState{}, err
	}
	return endpointState(rt.Name, rt.ARN, live, arn, version, status), nil
}

// promoteOutcome is how a promote reports the live endpoint: created when
// there was none, unchanged when it already served version, else updated.
func promoteOutcome(prior ResourceState, current, version string) (action deploy.Action, status string) {
	switch {
	case current == "":
		return deploy.ActionCreate, ResStatusCreated
	case current == version && prior.Status != "":
		return deploy.ActionNoChange, prior.Status
	case current == version:
		return deploy.ActionNoChange, ResStatusUpdated
	default:
		return deploy.ActionUpdate, ResStatusUpdated
	}
}

// deployedRuntimes returns the agent runtimes a state records as existing.
func deployedRuntimes(state *AdapterState) []ResourceState {
	var out []ResourceState
	for _, r := range state.Resources {
		if r.Type == ResTypeAgentRuntime && r.ARN != "" && r.Status != ResStatusFailed && r.Status != ResStatusPlanned {
			out = append(out, r)
		}
	}
	return out
}

// upsertResource replaces the entry with r's key, or appends r.
func upsertResource(resources []ResourceState, r ResourceState) []ResourceState {
	out := append([]ResourceState(nil), resources...)
	key := resourceKey(r.Type, r.Name)
	for i := range out {
		if resourceKey(out[i].Type, out[i].Name) == key {
			out[i] = r
			return out
		}
	}
	return append(out, r)
}

// ---------- plan ----------

// planEndpoints returns the endpoint changes a deploy makes to the named
// runtimes, given the prior state (nil when there is none). Each detail says
// which version the endpoint serves now, as last recorded, so a plan shows
// both live and candidate. Endpoints of runtimes no longer deployed are
// deleted.
func planEndpoints(runtimes []string, cfg *Config, prior *AdapterState) []deploy.ResourceChange {
	priorMap := map[string]ResourceState{}
	if prior != nil {
		for _, r := range prior.Resources {
			priorMap[resourceKey(r.Type, r.Name)] = r
		}
	}
	var changes []deploy.ResourceChange
	wanted := map[string]bool{}
	for _, rt := range runtimes {
		wanted[rt] = true
		changes = append(changes, planRuntimeEndpoints(rt, cfg, priorMap)...)
	}
	var gone []string
	for _, r := range priorMap {
		if r.Type == ResTypeRuntimeEndpoint && !wanted[runtimeOfEndpoint(r.Name)] {
			gone = append(gone, r.Name)
		}
	}
	sort.Strings(gone)
	for _, n := range gone {
		changes = append(changes, deploy.ResourceChange{
			Type: ResTypeRuntimeEndpoint, Name: n, Action: deploy.ActionDelete,
			Detail: fmt.Sprintf("Delete %s %s", ResTypeRuntimeEndpoint, n),
		})
	}
	return changes
}

// planRuntimeEndpoints plans one runtime's endpoints.
func planRuntimeEndpoints(rt string, cfg *Config, priorMap map[string]ResourceState) []deploy.ResourceChange {
	target := cfg.targetEndpoint()
	now := func(ep string) (string, bool) {
		r, ok := priorMap[resourceKey(ResTypeRuntimeEndpoint, endpointResourceName(rt, ep))]
		return r.Metadata[metaVersion], ok
	}
	describe := func(ep string) string {
		if v, ok := now(ep); ok && v != "" {
			return fmt.Sprintf(" (now version %s)", v)
		}
		return ""
	}
	var detail string
	switch cfg.stage() {
	case StageCandidate:
		detail = fmt.Sprintf("Stage the version this deploy creates on %s%s; %s does not change",
			target, describe(target), cfg.liveEndpoint())
	case StagePromote:
		candVersion, _ := now(cfg.candidateEndpoint())
		if candVersion == "" {
			candVersion = "?"
		}
		detail = fmt.Sprintf("Point %s at the version %s serves (version %s when last deployed)",
			target, cfg.candidateEndpoint(), candVersion)
		if v, ok := now(target); ok && v != "" {
			detail += fmt.Sprintf("; %s now serves version %s", target, v)
		}
	default:
		detail = fmt.Sprintf("Point %s at the version this deploy creates%s", target, describe(target))
	}
	action := deploy.ActionCreate
	if _, ok := now(target); ok {
		action = deploy.ActionUpdate
	}
	changes := []deploy.ResourceChange{{
		Type: ResTypeRuntimeEndpoint, Name: endpointResourceName(rt, target), Action: action, Detail: detail,
	}}
	for _, r := range sortedEndpoints(priorMap, rt) {
		ep := r.Metadata[metaEndpoint]
		if ep == target {
			continue
		}
		changes = append(changes, deploy.ResourceChange{
			Type: ResTypeRuntimeEndpoint, Name: r.Name, Action: deploy.ActionNoChange,
			Detail: fmt.Sprintf("%s stays on version %s", ep, r.Metadata[metaVersion]),
		})
	}
	return changes
}

// runtimeOfEndpoint returns the runtime part of a runtime_endpoint name.
func runtimeOfEndpoint(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return name
}

// withoutEndpoints returns a copy of state without its endpoint entries,
// which planEndpoints plans instead of the generic diff.
func withoutEndpoints(state *AdapterState) *AdapterState {
	if state == nil {
		return nil
	}
	out := *state
	out.Resources = nil
	for _, r := range state.Resources {
		if r.Type != ResTypeRuntimeEndpoint {
			out.Resources = append(out.Resources, r)
		}
	}
	return &out
}

// runtimeNamesIn returns the names of the runtimes a state records.
func runtimeNamesIn(state *AdapterState) []string {
	if state == nil {
		return nil
	}
	var out []string
	for _, r := range deployedRuntimes(state) {
		out = append(out, r.Name)
	}
	return out
}

// ---------- status ----------

// endpointVersionReader is implemented by a checker that can read an
// endpoint's version from the platform.
type endpointVersionReader interface {
	RuntimeEndpointVersion(ctx context.Context, runtimeARN, endpoint string) (string, error)
}

// endpointStatusDetail describes a runtime_endpoint entry for Status, with
// the version read from the platform when the checker can, and records that
// version in the entry.
func endpointStatusDetail(ctx context.Context, checker resourceChecker, res *ResourceState) string {
	ep := res.Metadata[metaEndpoint]
	version := res.Metadata[metaVersion]
	if reader, ok := checker.(endpointVersionReader); ok {
		if v, err := reader.RuntimeEndpointVersion(ctx, res.Metadata[metaRuntimeARN], ep); err == nil && v != "" {
			version = v
			setMetadata(res, metaVersion, v)
		}
	}
	return endpointDetail(ep, version)
}
