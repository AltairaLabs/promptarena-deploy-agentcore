package agentcore

import "encoding/json"

// partialApplyState builds the adapter state a failed apply returns. The CLI
// saves it in place of the stored state (the adapter advertises
// deploy.PartialApplyStateCapability), so it must describe everything that
// exists after the failure, not only what this apply touched: it is the prior
// state with this apply's results merged in (see mergePartialResources).
//
// When the apply created or updated nothing, priorState is returned verbatim.
// The CLI recognizes an unchanged prior state and keeps what it has stored.
func partialApplyState(priorState string, touched []ResourceState, packID, version string) (string, error) {
	var prior AdapterState
	if priorState != "" {
		if err := json.Unmarshal([]byte(priorState), &prior); err != nil {
			// A state this adapter cannot read cannot be merged into, and
			// replacing it would forget everything it records. Returning it
			// unchanged makes the CLI keep it, as Plan refuses it.
			return priorState, nil
		}
	}
	merged, wrote := mergePartialResources(prior.Resources, touched)
	if !wrote {
		return priorState, nil
	}
	stateJSON, err := json.Marshal(AdapterState{Resources: merged, PackID: packID, Version: version})
	if err != nil {
		return "", err
	}
	return string(stateJSON), nil
}

// mergePartialResources merges the resources a failed apply touched into the
// prior state's resources, keeping the prior order and appending new ones:
//
//   - a resource the apply created or updated replaces its prior entry;
//   - a resource whose create or update failed keeps its prior entry, or is
//     left out when it had none, because its create never happened (a create
//     that made the resource and failed after it is reported as created);
//   - a prior resource the apply never reached is kept as it was, unless its
//     entry never described a real resource (a dry run's planned entries, or
//     failed ones).
//
// wrote reports whether the apply created or updated anything.
func mergePartialResources(prior, touched []ResourceState) (merged []ResourceState, wrote bool) {
	index := make(map[string]int, len(prior)+len(touched))
	merged = make([]ResourceState, 0, len(prior)+len(touched))
	for _, r := range prior {
		key := resourceKey(r.Type, r.Name)
		if _, dup := index[key]; dup || r.Status == ResStatusPlanned || r.Status == ResStatusFailed {
			continue
		}
		index[key] = len(merged)
		merged = append(merged, r)
	}
	for _, r := range touched {
		if r.Status != ResStatusCreated && r.Status != ResStatusUpdated {
			continue
		}
		wrote = true
		key := resourceKey(r.Type, r.Name)
		if i, known := index[key]; known {
			merged[i] = r
			continue
		}
		index[key] = len(merged)
		merged = append(merged, r)
	}
	return merged, wrote
}
