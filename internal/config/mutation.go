package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tidwall/sjson"
)

// ErrInvalidConfigMutation marks a config mutation rejected before any state
// is written. HTTP callers map this error to a 400 response.
var ErrInvalidConfigMutation = errors.New("invalid config mutation")

type configChanges struct {
	set    map[string]any
	remove []string
}

func (c configChanges) empty() bool {
	return len(c.set) == 0 && len(c.remove) == 0
}

// writeConfigChanges applies sets and removals to one snapshot under one file
// lock and persists the result with one atomic rename. Removals run first and
// sets run second, so a set wins if a caller supplies overlapping paths.
func (s *ConfigStore) writeConfigChanges(scope Scope, changes configChanges) error {
	if changes.empty() {
		return nil
	}

	setKeys := make([]string, 0, len(changes.set))
	for key := range changes.set {
		setKeys = append(setKeys, key)
	}
	slices.Sort(setKeys)

	removeKeys := slices.Clone(changes.remove)
	slices.Sort(removeKeys)

	return s.atomicWrite(scope, func(data []byte) ([]byte, error) {
		value := string(data)
		for _, key := range removeKeys {
			var err error
			value, err = sjson.Delete(value, key)
			if err != nil {
				return nil, fmt.Errorf("failed to delete config field %s: %w", key, err)
			}
		}
		for _, key := range setKeys {
			var err error
			value, err = sjson.Set(value, key, changes.set[key])
			if err != nil {
				return nil, fmt.Errorf("failed to set config field %s: %w", key, err)
			}
		}
		return []byte(value), nil
	})
}

func (s *ConfigStore) updateChanges(scope Scope, mutate func(*Config) configChanges) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.updateChangesLocked(scope, mutate)
}

// updateChangesLocked is the shared transaction for typed config mutations.
// The clone and per-instance model pins become visible only after the disk
// mutation succeeds, so a failed write cannot leave memory ahead of disk.
func (s *ConfigStore) updateChangesLocked(scope Scope, mutate func(*Config) configChanges) error {
	next := s.Config().cloneForWrite()
	previousModelPins := maps.Clone(s.overrides.Models)
	changes := mutate(next)

	if !changes.empty() {
		if err := s.writeConfigChanges(scope, changes); err != nil {
			s.overrides.Models = previousModelPins
			return err
		}
	}

	s.setConfig(next)
	if !changes.empty() {
		if path, err := s.configPath(scope); err == nil {
			s.captureStalenessSnapshot(append(slices.Clone(s.loadedPaths), path))
		}
	}
	return nil
}

func validateConfigFields(fields map[string]any) error {
	if len(fields) == 0 {
		return fmt.Errorf("%w: fields must not be empty", ErrInvalidConfigMutation)
	}
	for key := range fields {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: field paths must not be empty", ErrInvalidConfigMutation)
		}
	}
	return nil
}

func validatePreferredModelUpdates(updates map[SelectedModelType]*SelectedModel) error {
	if len(updates) == 0 {
		return fmt.Errorf("%w: models must not be empty", ErrInvalidConfigMutation)
	}
	for modelType, model := range updates {
		if !modelType.Valid() {
			return fmt.Errorf("%w: unsupported model type %q", ErrInvalidConfigMutation, modelType)
		}
		if model == nil {
			continue
		}
		if strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" {
			return fmt.Errorf("%w: model %q requires provider and model", ErrInvalidConfigMutation, modelType)
		}
	}
	return nil
}

// UpdatePreferredModels atomically sets or removes multiple preferred-model
// slots. A nil value removes that slot and its per-instance pin while leaving
// recent-model history intact. Non-nil values use the same pin and recent-list
// behavior as UpdatePreferredModel.
func (s *ConfigStore) UpdatePreferredModels(scope Scope, updates map[SelectedModelType]*SelectedModel) error {
	if err := validatePreferredModelUpdates(updates); err != nil {
		return err
	}

	return s.updateChanges(scope, func(c *Config) configChanges {
		changes := configChanges{set: make(map[string]any)}
		for _, modelType := range []SelectedModelType{SelectedModelTypeLarge, SelectedModelTypeSmall} {
			model, ok := updates[modelType]
			if !ok {
				continue
			}
			if model == nil {
				delete(c.Models, modelType)
				delete(s.overrides.Models, modelType)
				changes.remove = append(changes.remove, fmt.Sprintf("models.%s", modelType))
				continue
			}
			maps.Copy(changes.set, s.updatePreferredModelFields(c, modelType, *model))
		}
		return changes
	})
}
