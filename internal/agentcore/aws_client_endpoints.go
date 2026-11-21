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
	if out.Status == types.AgentRuntimeEndpointStatusDeleting {
		return "", nil
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
func (c *realAWSClient) waitForEndpointServing(ctx context.Context, id, endpoint, version string) error {
	for range maxPollAttempts {
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
			return fmt.Errorf("endpoint %s/%s entered status %s: %s",
				id, endpoint, out.Status, aws.ToString(out.FailureReason))
		case types.AgentRuntimeEndpointStatusCreating, types.AgentRuntimeEndpointStatusUpdating,
			types.AgentRuntimeEndpointStatusDeleting:
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
	_, err := c.client.DeleteAgentRuntimeEndpoint(ctx, &bedrockagentcorecontrol.DeleteAgentRuntimeEndpointInput{
		AgentRuntimeId: aws.String(id),
		EndpointName:   aws.String(endpoint),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("DeleteAgentRuntimeEndpoint %s/%s: %w", id, endpoint, err)
	}
	for range maxPollAttempts {
		if _, err := c.getEndpoint(ctx, id, endpoint); err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("endpoint %s/%s still exists after %d attempts", id, endpoint, maxPollAttempts)
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
