package agentcore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

// staleFailurePolls is how many polls a failed status may be a leftover.
const staleFailurePolls = 3

// runtimeIDFromARN returns the runtime id in a runtime ARN, or in a runtime
// endpoint ARN (…:runtime/<id>/runtime-endpoint/<name>).
func runtimeIDFromARN(arn string) string {
	id := extractResourceID(arn, "runtime")
	if i := strings.Index(id, "/"); i >= 0 {
		id = id[:i]
	}
	return id
}

// runtimeDescription returns the description of the runtime's latest version,
// or "" when it cannot be read: a lost description is not worth failing an
// update over, and the pack version tag is written either way.
func (c *realAWSClient) runtimeDescription(ctx context.Context, id string) string {
	out, err := c.client.GetAgentRuntime(ctx, &bedrockagentcorecontrol.GetAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
	})
	if err != nil {
		log.Printf("agentcore: could not read runtime %s description (%v); writing the pack version alone", id, err)
		return ""
	}
	return aws.ToString(out.Description)
}

// RuntimeVersion returns the runtime's latest version.
func (c *realAWSClient) RuntimeVersion(ctx context.Context, runtimeARN string) (string, error) {
	id := runtimeIDFromARN(runtimeARN)
	if id == "" {
		return "", fmt.Errorf("could not extract runtime ID from ARN %q", runtimeARN)
	}
	out, err := c.client.GetAgentRuntime(ctx, &bedrockagentcorecontrol.GetAgentRuntimeInput{
		AgentRuntimeId: aws.String(id),
	})
	if err != nil {
		return "", fmt.Errorf("GetAgentRuntime %s: %w", id, err)
	}
	return aws.ToString(out.AgentRuntimeVersion), nil
}

// RuntimeEndpointVersion returns the version a named endpoint serves, or ""
// when the runtime has no such endpoint (or it is being deleted).
func (c *realAWSClient) RuntimeEndpointVersion(
	ctx context.Context, runtimeARN, endpoint string,
) (string, error) {
	id := runtimeIDFromARN(runtimeARN)
	if id == "" {
		return "", fmt.Errorf("could not extract runtime ID from ARN %q", runtimeARN)
	}
	out, err := c.getEndpoint(ctx, id, endpoint)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	switch out.Status {
	case types.AgentRuntimeEndpointStatusDeleting:
		return "", nil
	case types.AgentRuntimeEndpointStatusCreating, types.AgentRuntimeEndpointStatusUpdating:
		// Moving: what it serves is about to change, so it is not yet a
		// version to read (a promote would take the outgoing one).
		return "", fmt.Errorf("endpoint %s/%s is %s; try again when it is READY", id, endpoint, out.Status)
	case types.AgentRuntimeEndpointStatusReady, types.AgentRuntimeEndpointStatusCreateFailed,
		types.AgentRuntimeEndpointStatusUpdateFailed:
	}
	return servedVersion(out), nil
}

// servedVersion is the version an endpoint serves: its live version, or,
// before it has one, the version it is moving to.
func servedVersion(out *bedrockagentcorecontrol.GetAgentRuntimeEndpointOutput) string {
	if v := aws.ToString(out.LiveVersion); v != "" {
		return v
	}
	return aws.ToString(out.TargetVersion)
}

func (c *realAWSClient) getEndpoint(
	ctx context.Context, runtimeID, endpoint string,
) (*bedrockagentcorecontrol.GetAgentRuntimeEndpointOutput, error) {
	out, err := c.client.GetAgentRuntimeEndpoint(ctx, &bedrockagentcorecontrol.GetAgentRuntimeEndpointInput{
		AgentRuntimeId: aws.String(runtimeID),
		EndpointName:   aws.String(endpoint),
	})
	if err != nil {
		return nil, fmt.Errorf("GetAgentRuntimeEndpoint %s/%s: %w", runtimeID, endpoint, err)
	}
	return out, nil
}

// PointRuntimeEndpoint creates the endpoint at version, or repoints it, and
// waits until it serves version. An endpoint already serving it is left alone.
func (c *realAWSClient) PointRuntimeEndpoint(
	ctx context.Context, runtimeARN, endpoint, version string,
) (string, error) {
	id := runtimeIDFromARN(runtimeARN)
	if id == "" {
		return "", fmt.Errorf("could not extract runtime ID from ARN %q", runtimeARN)
	}
	cur, err := c.getEndpoint(ctx, id, endpoint)
	switch {
	case err != nil && isNotFound(err):
		return c.createEndpoint(ctx, id, endpoint, version)
	case err != nil:
		return "", err
	case cur.Status == types.AgentRuntimeEndpointStatusReady &&
		aws.ToString(cur.LiveVersion) == version &&
		(cur.TargetVersion == nil || aws.ToString(cur.TargetVersion) == version):
		return aws.ToString(cur.AgentRuntimeEndpointArn), nil
	}
	out, err := c.client.UpdateAgentRuntimeEndpoint(ctx, &bedrockagentcorecontrol.UpdateAgentRuntimeEndpointInput{
		AgentRuntimeId:      aws.String(id),
		EndpointName:        aws.String(endpoint),
		AgentRuntimeVersion: aws.String(version),
	})
	if err != nil {
		return "", fmt.Errorf("UpdateAgentRuntimeEndpoint %s/%s: %w", id, endpoint, err)
	}
	arn := aws.ToString(out.AgentRuntimeEndpointArn)
	return arn, c.waitForEndpointServing(ctx, id, endpoint, version)
}

func (c *realAWSClient) createEndpoint(ctx context.Context, id, endpoint, version string) (string, error) {
	input := &bedrockagentcorecontrol.CreateAgentRuntimeEndpointInput{
		AgentRuntimeId:      aws.String(id),
		Name:                aws.String(endpoint),
		AgentRuntimeVersion: aws.String(version),
	}
	if c.cfg != nil && len(c.cfg.ResourceTags) > 0 {
		input.Tags = c.cfg.ResourceTags
	}
	out, err := c.client.CreateAgentRuntimeEndpoint(ctx, input)
	if err != nil {
		return "", fmt.Errorf("CreateAgentRuntimeEndpoint %s/%s: %w", id, endpoint, err)
	}
	arn := aws.ToString(out.AgentRuntimeEndpointArn)
	return arn, c.waitForEndpointServing(ctx, id, endpoint, version)
}

// waitForEndpointServing polls until the endpoint is READY on version.
//
// A failed status counts only once the endpoint has been seen moving, or
// after a few polls: right after an update, an endpoint an earlier attempt
// left failed can still report that old failure.
func (c *realAWSClient) waitForEndpointServing(ctx context.Context, id, endpoint, version string) error {
	moved := false
	for attempt := range maxPollAttempts {
		out, err := c.getEndpoint(ctx, id, endpoint)
		if err != nil {
			return err
		}
		switch out.Status {
		case types.AgentRuntimeEndpointStatusReady:
			if aws.ToString(out.LiveVersion) == version {
				return nil
			}
		case types.AgentRuntimeEndpointStatusCreateFailed, types.AgentRuntimeEndpointStatusUpdateFailed:
			if moved || attempt >= staleFailurePolls {
				return fmt.Errorf("endpoint %s/%s entered status %s: %s",
					id, endpoint, out.Status, aws.ToString(out.FailureReason))
			}
		case types.AgentRuntimeEndpointStatusCreating, types.AgentRuntimeEndpointStatusUpdating,
			types.AgentRuntimeEndpointStatusDeleting:
			moved = true
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("endpoint %s/%s did not serve version %s after %d attempts", id, endpoint, version, maxPollAttempts)
}

// deleteRuntimeEndpoint deletes a named endpoint and waits until it is gone,
// so the runtime it belongs to can be deleted after it.
func (c *realAWSClient) deleteRuntimeEndpoint(ctx context.Context, res ResourceState) error {
	id := runtimeIDFromARN(res.Metadata[metaRuntimeARN])
	if id == "" {
		id = runtimeIDFromARN(res.ARN)
	}
	endpoint := res.Metadata[metaEndpoint]
	if id == "" || endpoint == "" {
		return fmt.Errorf("endpoint %q records no runtime or endpoint name", res.Name)
	}
	return deleteEndpointAndWait(ctx, c.client, defaultEndpointDeleteWait, id, endpoint)
}

// endpointAPI is the part of the AgentCore control client that deleting an
// endpoint uses, so the wait can be tested against a slow delete.
type endpointAPI interface {
	DeleteAgentRuntimeEndpoint(
		ctx context.Context, in *bedrockagentcorecontrol.DeleteAgentRuntimeEndpointInput,
		optFns ...func(*bedrockagentcorecontrol.Options),
	) (*bedrockagentcorecontrol.DeleteAgentRuntimeEndpointOutput, error)
	GetAgentRuntimeEndpoint(
		ctx context.Context, in *bedrockagentcorecontrol.GetAgentRuntimeEndpointInput,
		optFns ...func(*bedrockagentcorecontrol.Options),
	) (*bedrockagentcorecontrol.GetAgentRuntimeEndpointOutput, error)
}

// Endpoint deletion bounds. AWS documents no duration for it, and on real
// AgentCore it has outlasted the five-minute window every other wait uses:
// endpoints were seen still DELETING after five minutes and gone a few
// minutes later. Twenty minutes is a generous bound on that, and a slower
// poll keeps the call count the same order as before.
const (
	endpointDeleteTimeout      = 20 * time.Minute
	endpointDeletePollInterval = 10 * time.Second
)

// endpointStatusDeleteFailed is a status the API documents for endpoint
// deletion that the pinned SDK has no constant for yet.
const endpointStatusDeleteFailed types.AgentRuntimeEndpointStatus = "DELETE_FAILED"

// endpointDeleteWait is how long, and how often, to poll for a deleted
// endpoint to disappear.
type endpointDeleteWait struct {
	timeout  time.Duration
	interval time.Duration
}

var defaultEndpointDeleteWait = endpointDeleteWait{
	timeout:  endpointDeleteTimeout,
	interval: endpointDeletePollInterval,
}

// deleteEndpointAndWait deletes a named endpoint and waits until
// GetAgentRuntimeEndpoint reports it not found. An endpoint already gone
// counts as deleted.
func deleteEndpointAndWait(
	ctx context.Context, api endpointAPI, wait endpointDeleteWait, id, endpoint string,
) error {
	_, err := api.DeleteAgentRuntimeEndpoint(ctx, &bedrockagentcorecontrol.DeleteAgentRuntimeEndpointInput{
		AgentRuntimeId: aws.String(id),
		EndpointName:   aws.String(endpoint),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("DeleteAgentRuntimeEndpoint %s/%s: %w", id, endpoint, err)
	}
	return waitForEndpointGone(ctx, api, wait, id, endpoint)
}

// waitForEndpointGone polls until the endpoint is not found, it reports a
// failed delete, the wait runs out, or ctx is done.
func waitForEndpointGone(
	ctx context.Context, api endpointAPI, wait endpointDeleteWait, id, endpoint string,
) error {
	deadline := time.Now().Add(wait.timeout)
	for {
		out, err := api.GetAgentRuntimeEndpoint(ctx, &bedrockagentcorecontrol.GetAgentRuntimeEndpointInput{
			AgentRuntimeId: aws.String(id),
			EndpointName:   aws.String(endpoint),
		})
		switch {
		case err != nil && isNotFound(err):
			return nil
		case err != nil:
			return fmt.Errorf("GetAgentRuntimeEndpoint %s/%s: %w", id, endpoint, err)
		case out.Status == endpointStatusDeleteFailed:
			return fmt.Errorf("endpoint %s/%s entered status %s: %s",
				id, endpoint, out.Status, aws.ToString(out.FailureReason))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("endpoint %s/%s was still %s when the delete wait ended after %s; "+
				"AgentCore may still be deleting it, so run destroy again to finish",
				id, endpoint, out.Status, wait.timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for endpoint %s/%s to be deleted: %w", id, endpoint, ctx.Err())
		case <-time.After(wait.interval):
		}
	}
}

// checkRuntimeEndpoint reports a named endpoint's health.
func (c *realAWSClient) checkRuntimeEndpoint(ctx context.Context, res ResourceState) (string, error) {
	id := runtimeIDFromARN(res.Metadata[metaRuntimeARN])
	if id == "" {
		id = runtimeIDFromARN(res.ARN)
	}
	out, err := c.getEndpoint(ctx, id, res.Metadata[metaEndpoint])
	if err != nil {
		if isNotFound(err) {
			return StatusMissing, nil
		}
		return StatusUnhealthy, err
	}
	switch out.Status {
	case types.AgentRuntimeEndpointStatusDeleting:
		return StatusMissing, nil
	case types.AgentRuntimeEndpointStatusReady:
		return StatusHealthy, nil
	case types.AgentRuntimeEndpointStatusCreating, types.AgentRuntimeEndpointStatusCreateFailed,
		types.AgentRuntimeEndpointStatusUpdating, types.AgentRuntimeEndpointStatusUpdateFailed:
	}
	return StatusUnhealthy, nil
}
