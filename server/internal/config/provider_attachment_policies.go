package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var ErrProviderConfigChanged = errors.New("provider configuration changed")

// ApplyProviderAttachmentPoliciesCAS verifies the exact route and mutable plan
// policy under the settings lock, applies the complete selection batch in one
// durable write, and returns the immutable settings snapshot used for dispatch.
func (s *Store) ApplyProviderAttachmentPoliciesCAS(
	connectionID, model, expectedRoutingFingerprint, expectedPolicyFingerprint string,
	policies []ProviderAttachmentPolicy,
) (AppSettings, []ProviderAttachmentPolicy, error) {
	if s == nil {
		return AppSettings{}, nil, errors.New("settings store is unavailable")
	}
	connectionID = strings.TrimSpace(connectionID)
	model = strings.TrimSpace(model)
	expectedRoutingFingerprint = strings.TrimSpace(expectedRoutingFingerprint)
	expectedPolicyFingerprint = strings.TrimSpace(expectedPolicyFingerprint)
	if connectionID == "" || model == "" || expectedRoutingFingerprint == "" || expectedPolicyFingerprint == "" {
		return AppSettings{}, nil, errors.New("connection, model, and expected fingerprints are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings.Clone()
	for index := range next.ProviderConfigs {
		cfg := &next.ProviderConfigs[index]
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		target := *cfg
		target.Model = model
		if ProviderConfigFingerprint(target) != expectedRoutingFingerprint ||
			ProviderPlanPolicyFingerprint(target) != expectedPolicyFingerprint {
			return AppSettings{}, nil, ErrProviderConfigChanged
		}
		created := make([]ProviderAttachmentPolicy, 0, len(policies))
		now := time.Now().UTC().UnixMilli()
		for _, policy := range policies {
			policy.Class = strings.ToLower(strings.TrimSpace(policy.Class))
			policy.Action = strings.ToLower(strings.TrimSpace(policy.Action))
			policy.APISurface = strings.ToLower(strings.TrimSpace(policy.APISurface))
			if policy.Class == "" || policy.Action == "" {
				return AppSettings{}, nil, errors.New("attachment policy class and action are required")
			}
			if policy.APISurface == "" {
				policy.APISurface = DeriveProviderAPISurface(target)
			}
			if policy.APISurface == "" {
				return AppSettings{}, nil, errors.New("attachment policy api surface is required")
			}
			exists := false
			for _, existing := range cfg.AttachmentPolicies {
				if strings.EqualFold(strings.TrimSpace(existing.Class), policy.Class) &&
					strings.EqualFold(strings.TrimSpace(existing.Action), policy.Action) &&
					strings.EqualFold(strings.TrimSpace(existing.APISurface), policy.APISurface) {
					exists = true
					break
				}
			}
			if exists {
				continue
			}
			if strings.TrimSpace(policy.ID) == "" {
				policy.ID = attachmentPolicyID(connectionID, policy)
			}
			if policy.CreatedAt <= 0 {
				policy.CreatedAt = now
			}
			cfg.AttachmentPolicies = append(cfg.AttachmentPolicies, policy)
			created = append(created, policy)
		}
		if len(created) > 0 {
			if err := s.saveLocked(next); err != nil {
				return AppSettings{}, nil, err
			}
		} else {
			next = s.settings.Clone()
		}
		return next.Clone(), append([]ProviderAttachmentPolicy(nil), created...), nil
	}
	return AppSettings{}, nil, ErrProviderConfigChanged
}

// UpsertProviderAttachmentPolicy records connection-scoped attachment consent.
// Retries are idempotent by (class, action, apiSurface), as required by run-plan
// finalization. The update is serialized with full settings saves so neither can
// silently overwrite the other in memory or on disk.
func (s *Store) UpsertProviderAttachmentPolicy(connectionID string, policy ProviderAttachmentPolicy) (ProviderAttachmentPolicy, error) {
	persisted, _, err := s.UpsertProviderAttachmentPolicyTracked(connectionID, policy)
	return persisted, err
}

// UpsertProviderAttachmentPolicyTracked has the same idempotent contract as
// UpsertProviderAttachmentPolicy and additionally reports whether this call
// created the durable policy. The existing-match check and insert happen under
// one settings lock so cross-store compensation can never claim ownership of a
// policy created by another concurrent submission.
func (s *Store) UpsertProviderAttachmentPolicyTracked(connectionID string, policy ProviderAttachmentPolicy) (ProviderAttachmentPolicy, bool, error) {
	if s == nil {
		return ProviderAttachmentPolicy{}, false, errors.New("settings store is unavailable")
	}
	connectionID = strings.TrimSpace(connectionID)
	policy.Class = strings.ToLower(strings.TrimSpace(policy.Class))
	policy.Action = strings.ToLower(strings.TrimSpace(policy.Action))
	policy.APISurface = strings.ToLower(strings.TrimSpace(policy.APISurface))
	if connectionID == "" {
		return ProviderAttachmentPolicy{}, false, errors.New("connection id is required")
	}
	if policy.Class == "" {
		return ProviderAttachmentPolicy{}, false, errors.New("attachment policy class is required")
	}
	if policy.Action == "" {
		return ProviderAttachmentPolicy{}, false, errors.New("attachment policy action is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings.Clone()
	for index := range next.ProviderConfigs {
		cfg := &next.ProviderConfigs[index]
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		if policy.APISurface == "" {
			policy.APISurface = DeriveProviderAPISurface(*cfg)
		}
		if policy.APISurface == "" {
			return ProviderAttachmentPolicy{}, false, errors.New("attachment policy api surface is required")
		}
		for _, existing := range cfg.AttachmentPolicies {
			if strings.EqualFold(strings.TrimSpace(existing.Class), policy.Class) &&
				strings.EqualFold(strings.TrimSpace(existing.Action), policy.Action) &&
				strings.EqualFold(strings.TrimSpace(existing.APISurface), policy.APISurface) {
				return existing, false, nil
			}
		}
		if strings.TrimSpace(policy.ID) == "" {
			policy.ID = attachmentPolicyID(connectionID, policy)
		}
		if policy.CreatedAt <= 0 {
			policy.CreatedAt = time.Now().UTC().UnixMilli()
		}
		cfg.AttachmentPolicies = append(cfg.AttachmentPolicies, policy)
		if err := s.saveLocked(next); err != nil {
			return ProviderAttachmentPolicy{}, false, err
		}
		return policy, true, nil
	}
	return ProviderAttachmentPolicy{}, false, errors.New("provider connection not found")
}

// DeleteProviderAttachmentPolicy removes exactly one policy ID from exactly one
// provider connection. Missing policies are a successful no-op so compensation
// is safe to retry; a policy with the same ID on another connection is never
// touched.
func (s *Store) DeleteProviderAttachmentPolicy(connectionID, policyID string) (bool, error) {
	if s == nil {
		return false, errors.New("settings store is unavailable")
	}
	connectionID = strings.TrimSpace(connectionID)
	policyID = strings.TrimSpace(policyID)
	if connectionID == "" {
		return false, errors.New("connection id is required")
	}
	if policyID == "" {
		return false, errors.New("attachment policy id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.settings.Clone()
	for index := range next.ProviderConfigs {
		cfg := &next.ProviderConfigs[index]
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		removeAt := -1
		for policyIndex, existing := range cfg.AttachmentPolicies {
			if strings.TrimSpace(existing.ID) == policyID {
				removeAt = policyIndex
				break
			}
		}
		if removeAt < 0 {
			return false, nil
		}
		policies := make([]ProviderAttachmentPolicy, 0, len(cfg.AttachmentPolicies)-1)
		policies = append(policies, cfg.AttachmentPolicies[:removeAt]...)
		policies = append(policies, cfg.AttachmentPolicies[removeAt+1:]...)
		if len(policies) == 0 {
			cfg.AttachmentPolicies = nil
		} else {
			cfg.AttachmentPolicies = policies
		}
		if err := s.saveLocked(next); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, errors.New("provider connection not found")
}

func attachmentPolicyID(connectionID string, policy ProviderAttachmentPolicy) string {
	material := strings.Join([]string{connectionID, policy.Class, policy.Action, policy.APISurface}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return "policy_" + hex.EncodeToString(sum[:12])
}
