package agent

import (
	"encoding/json"
	"errors"
)

func preserveExtraBodyLayers(options map[string]any, layers ...[]byte) error {
	var body any
	present := false
	for _, layer := range layers {
		var fields map[string]any
		if err := json.Unmarshal(layer, &fields); err != nil {
			return err
		}
		value, exists := fields["extra_body"]
		if !exists {
			continue
		}
		present = true
		current, currentOK := body.(map[string]any)
		next, nextOK := value.(map[string]any)
		if value != nil && !nextOK {
			return errors.New("invalid provider option extra_body: must be an object")
		}
		if currentOK && nextOK {
			body = withProviderDefaults(next, current)
		} else {
			body = value
		}
	}
	if present {
		options["extra_body"] = body
	}
	return nil
}

func mergeExtraBodyDefaults(options, defaults map[string]any) error {
	configured, present := options["extra_body"]
	if present && configured == nil {
		return nil
	}
	if present {
		body, ok := configured.(map[string]any)
		if !ok {
			return errors.New("invalid provider option extra_body: must be an object")
		}
		options["extra_body"] = withProviderDefaults(body, defaults)
		return nil
	}
	if len(defaults) > 0 {
		options["extra_body"] = defaults
	}
	return nil
}

func withProviderDefaults(configured, defaults map[string]any) map[string]any {
	merged := make(map[string]any, len(configured)+len(defaults))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range configured {
		child, childOK := value.(map[string]any)
		fallback, fallbackOK := merged[key].(map[string]any)
		if childOK && fallbackOK {
			merged[key] = withProviderDefaults(child, fallback)
		} else {
			merged[key] = value
		}
	}
	return merged
}
