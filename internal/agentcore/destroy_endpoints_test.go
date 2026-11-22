package agentcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/AltairaLabs/promptarena/v2/deploy"
)

// slowEndpointAPI models AgentCore deleting an endpoint: it reports DELETING
// for deletingPolls polls after the delete, then not found.
type slowEndpointAPI struct {
	deleteErr     error
	getErr        error
	deletingPolls int
	status        types.AgentRuntimeEndpointStatus // reported while deleting; DELETING when empty

	gets atomic.Int32
}

func (f *slowEndpointAPI) DeleteAgentRuntimeEndpoint(
	_ context.Context, _ *bedrockagentcorecontrol.DeleteAgentRuntimeEndpointInput,
	_ ...func(*bedrockagentcorecontrol.Options),
) (*bedrockagentcorecontrol.DeleteAgentRuntimeEndpointOutput, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &bedrockagentcorecontrol.DeleteAgentRuntimeEndpointOutput{}, nil
}

func (f *slowEndpointAPI) GetAgentRuntimeEndpoint(
	_ context.Context, _ *bedrockagentcorecontrol.GetAgentRuntimeEndpointInput,
	_ ...func(*bedrockagentcorecontrol.Options),
) (*bedrockagentcorecontrol.GetAgentRuntimeEndpointOutput, error) {
	n := int(f.gets.Add(1))
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.deletingPolls >= 0 && n > f.deletingPolls {
		return nil, notFoundErr()
	}
	status := f.status
	if status == "" {
		status = types.AgentRuntimeEndpointStatusDeleting
	}
	return &bedrockagentcorecontrol.GetAgentRuntimeEndpointOutput{
		Status:        status,
		FailureReason: aws.String("internal error"),
	}, nil
}

func notFoundErr() error {
	return &types.ResourceNotFoundException{Message: aws.String("endpoint not found")}
}

func fastWait(timeout time.Duration) endpointDeleteWait {
	return endpointDeleteWait{timeout: timeout, interval: time.Millisecond}
}

// The wait used to give up after 60 polls. A delete that outlasts that is
// normal on AgentCore, so the wait must run on until the endpoint is gone.
func TestDeleteEndpointAndWait_OutlastsSlowDelete(t *testing.T) {
	api := &slowEndpointAPI{deletingPolls: 150}
	err := deleteEndpointAndWait(context.Background(), api, fastWait(10*time.Second), "rt-abc", "live")
	if err != nil {
		t.Fatalf("deleteEndpointAndWait: %v", err)
	}
	if got := api.gets.Load(); got != 151 {
		t.Errorf("polled %d times, want 151 (150 DELETING, then not found)", got)
	}
}

func TestDeleteEndpointAndWait_TimesOutAsATimeout(t *testing.T) {
	api := &slowEndpointAPI{deletingPolls: -1}
	err := deleteEndpointAndWait(context.Background(), api, fastWait(20*time.Millisecond), "rt-abc", "live")
	if err == nil {
		t.Fatal("expected an error for an endpoint that never goes away")
	}
	if !strings.Contains(err.Error(), "still DELETING") {
		t.Errorf("error %q does not report the last status", err)
	}
	category, hint := classifyAWSError(err)
	if category != ErrCategoryTimeout || hint != hintRetryOrTimeout {
		t.Errorf("classified as (%q, %q), want a timeout", category, hint)
	}
}

func TestDeleteEndpointAndWait_DeleteFailedStops(t *testing.T) {
	api := &slowEndpointAPI{deletingPolls: -1, status: endpointStatusDeleteFailed}
	err := deleteEndpointAndWait(context.Background(), api, fastWait(10*time.Second), "rt-abc", "live")
	if err == nil || !strings.Contains(err.Error(), "DELETE_FAILED: internal error") {
		t.Fatalf("error = %v, want the DELETE_FAILED reason", err)
	}
	if got := api.gets.Load(); got != 1 {
		t.Errorf("polled %d times after DELETE_FAILED, want 1", got)
	}
}

func TestDeleteEndpointAndWait_AlreadyGone(t *testing.T) {
	api := &slowEndpointAPI{deleteErr: notFoundErr()}
	if err := deleteEndpointAndWait(context.Background(), api, fastWait(time.Second), "rt-abc", "live"); err != nil {
		t.Fatalf("deleteEndpointAndWait: %v", err)
	}
	if got := api.gets.Load(); got != 0 {
		t.Errorf("polled %d times for an endpoint already gone", got)
	}
}

func TestDeleteEndpointAndWait_APIErrors(t *testing.T) {
	boom := errors.New("throttled")
	tests := []struct {
		name string
		api  *slowEndpointAPI
		want string
	}{
		{"delete", &slowEndpointAPI{deleteErr: boom}, "DeleteAgentRuntimeEndpoint rt-abc/live"},
		{"get", &slowEndpointAPI{getErr: boom}, "GetAgentRuntimeEndpoint rt-abc/live"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := deleteEndpointAndWait(context.Background(), tt.api, fastWait(time.Second), "rt-abc", "live")
			if !errors.Is(err, boom) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q wrapping %v", err, tt.want, boom)
			}
		})
	}
}

func TestDeleteEndpointAndWait_StopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api := &slowEndpointAPI{deletingPolls: -1}
	err := deleteEndpointAndWait(ctx, api, endpointDeleteWait{timeout: time.Hour, interval: time.Hour}, "rt-abc", "live")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// endpointRuntimeDestroyer models a runtime whose named endpoints take a
// while to delete. Deleting the runtime while any endpoint remains fails
// the way AgentCore does, with a 409.
type endpointRuntimeDestroyer struct {
	deleteTime  time.Duration
	failOnNames map[string]bool // endpoints whose delete times out and stays

	mu        sync.Mutex
	remaining map[string]bool
	inFlight  int
	maxFlight int
}

func (d *endpointRuntimeDestroyer) DeleteResource(_ context.Context, res ResourceState) error {
	switch res.Type {
	case ResTypeRuntimeEndpoint:
		d.mu.Lock()
		d.inFlight++
		d.maxFlight = max(d.maxFlight, d.inFlight)
		d.mu.Unlock()
		time.Sleep(d.deleteTime)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.inFlight--
		if d.failOnNames[res.Name] {
			return fmt.Errorf("endpoint %s was still DELETING when the delete wait ended after 20m0s", res.Name)
		}
		delete(d.remaining, res.Name)
		return nil
	case ResTypeAgentRuntime:
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(d.remaining) > 0 {
			return errors.New("DeleteAgentRuntime: ConflictException: This agent has endpoints " +
				"that must be deleted before the agent can be removed")
		}
	}
	return nil
}

func (d *endpointRuntimeDestroyer) DeleteGatewayTarget(context.Context, string, string) error {
	return nil
}

func rolloutDestroyState() *AdapterState {
	rtARN := "arn:aws:bedrock-agentcore:us-east-1:123456789012:runtime/acproj-abc"
	ep := func(name string) ResourceState {
		return ResourceState{
			Type: ResTypeRuntimeEndpoint, Name: "acproj/" + name,
			ARN:      rtARN + "/runtime-endpoint/" + name,
			Metadata: map[string]string{metaRuntimeARN: rtARN, metaEndpoint: name},
		}
	}
	return &AdapterState{Resources: []ResourceState{
		{Type: ResTypeAgentRuntime, Name: "acproj", ARN: rtARN},
		ep("live"),
		ep("candidate"),
	}}
}

func destroyWith(t *testing.T, d resourceDestroyer, state *AdapterState) ([]*deploy.DestroyEvent, error) {
	t.Helper()
	p := &Provider{destroyerFunc: func(context.Context, *Config) (resourceDestroyer, error) { return d, nil }}
	var events []*deploy.DestroyEvent
	err := p.Destroy(context.Background(), &deploy.DestroyRequest{
		DeployConfig: validDestroyConfig(),
		PriorState:   mustJSON(t, state),
	}, func(e *deploy.DestroyEvent) error {
		events = append(events, e)
		return nil
	})
	return events, err
}

func newEndpointRuntimeDestroyer(deleteTime time.Duration, failOn ...string) *endpointRuntimeDestroyer {
	d := &endpointRuntimeDestroyer{
		deleteTime:  deleteTime,
		failOnNames: map[string]bool{},
		remaining:   map[string]bool{"acproj/live": true, "acproj/candidate": true},
	}
	for _, n := range failOn {
		d.failOnNames[n] = true
	}
	return d
}

// Both endpoints are deleted together, and the runtime only once both are
// gone, so the runtime delete succeeds.
func TestDestroy_DeletesEndpointsConcurrentlyThenRuntime(t *testing.T) {
	d := newEndpointRuntimeDestroyer(50 * time.Millisecond)
	events, err := destroyWith(t, d, rolloutDestroyState())
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if d.maxFlight != 2 {
		t.Errorf("at most %d endpoint deletes ran at once, want 2", d.maxFlight)
	}
	var deleted []string
	for _, e := range events {
		if e.Resource != nil && e.Resource.Status == ResStatusDeleted {
			deleted = append(deleted, e.Resource.Name)
		}
	}
	want := []string{"acproj/live", "acproj/candidate", "acproj"}
	if strings.Join(deleted, ",") != strings.Join(want, ",") {
		t.Errorf("deleted %v, want %v (endpoints in state order, then the runtime)", deleted, want)
	}
	if last := events[len(events)-1]; last.Type != "complete" {
		t.Errorf("last event = %q, want complete", last.Type)
	}
}

// The reported run: the endpoint deletes time out, so the runtime delete
// hits a 409. Destroy must fail, naming every survivor, and not complete.
func TestDestroy_FailsWhenEndpointsAndRuntimeSurvive(t *testing.T) {
	d := newEndpointRuntimeDestroyer(time.Millisecond, "acproj/live", "acproj/candidate")
	events, err := destroyWith(t, d, rolloutDestroyState())
	if err == nil {
		t.Fatal("Destroy returned nil although nothing was deleted")
	}
	for _, name := range []string{`"acproj/live"`, `"acproj/candidate"`, `agent_runtime "acproj"`} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
	var hints []string
	for _, e := range events {
		if e.Type == "complete" {
			t.Errorf("a failed destroy emitted complete: %q", e.Message)
		}
		if e.Type == "error" {
			hints = append(hints, e.Message)
			if strings.Contains(e.Message, hintCheckNetwork) {
				t.Errorf("error event carries the network hint: %q", e.Message)
			}
		}
	}
	if len(hints) != 3 {
		t.Fatalf("got %d error events, want 3: %v", len(hints), hints)
	}
	if !strings.Contains(hints[2], hintDependentResource) {
		t.Errorf("runtime 409 hint = %q, want %q", hints[2], hintDependentResource)
	}
}

func TestDestroy_UnorderedResourceFailureFailsDestroy(t *testing.T) {
	d := &failingDestroyer{failOn: map[string]bool{"custom_thing": true}}
	state := &AdapterState{Resources: []ResourceState{{Type: "custom_thing", Name: "c-1", ARN: "arn:custom"}}}
	events, err := destroyWith(t, d, state)
	if err == nil || !strings.Contains(err.Error(), `custom_thing "c-1"`) {
		t.Fatalf("Destroy error = %v, want one naming custom_thing \"c-1\"", err)
	}
	for _, e := range events {
		if e.Resource != nil && e.Resource.Status == ResStatusDeleted {
			t.Errorf("a failed delete was reported deleted: %q", e.Message)
		}
	}
}

func TestDestroy_SkipsFailedEndpointsInConcurrentGroup(t *testing.T) {
	d := newEndpointRuntimeDestroyer(time.Millisecond)
	state := rolloutDestroyState()
	state.Resources[2].Status = ResStatusFailed // candidate was never created
	delete(d.remaining, "acproj/candidate")
	if _, err := destroyWith(t, d, state); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if d.maxFlight != 1 {
		t.Errorf("%d endpoint deletes ran, want 1 (the failed one is skipped)", d.maxFlight)
	}
}
