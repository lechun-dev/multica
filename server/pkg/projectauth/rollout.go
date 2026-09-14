package projectauth

import (
	"fmt"
	"strings"
)

// RolloutPhase is the deployment-wide authorization activation boundary. The
// ordered phases deliberately separate comparison, read enforcement, ordinary
// ACL writes, and restricted-mode writes so rollback can stop mutations while
// retaining the reader that protects already-restricted tasks.
type RolloutPhase string

const (
	RolloutOff        RolloutPhase = "off"
	RolloutShadow     RolloutPhase = "shadow"
	RolloutReader     RolloutPhase = "reader"
	RolloutWriter     RolloutPhase = "writer"
	RolloutRestricted RolloutPhase = "restricted"
)

func ParseRolloutPhase(raw string) (RolloutPhase, error) {
	phase := RolloutPhase(strings.ToLower(strings.TrimSpace(raw)))
	if phase == "" {
		phase = RolloutOff
	}
	switch phase {
	case RolloutOff, RolloutShadow, RolloutReader, RolloutWriter, RolloutRestricted:
		return phase, nil
	default:
		return RolloutOff, fmt.Errorf("invalid project permission rollout phase %q", raw)
	}
}

// LegacyRolloutPhase keeps PROJECT_PERMISSION_ENABLED backwards compatible.
// An explicit PROJECT_PERMISSION_ROLLOUT_PHASE should be preferred.
func LegacyRolloutPhase(enabled bool) RolloutPhase {
	if enabled {
		return RolloutRestricted
	}
	return RolloutOff
}

func (p RolloutPhase) ShadowEnabled() bool { return p == RolloutShadow }
func (p RolloutPhase) ReaderEnabled() bool {
	return p == RolloutReader || p == RolloutWriter || p == RolloutRestricted
}
func (p RolloutPhase) WriterEnabled() bool           { return p == RolloutWriter || p == RolloutRestricted }
func (p RolloutPhase) RestrictedWritesEnabled() bool { return p == RolloutRestricted }

// mutationEnabled is the package-level write boundary. RolloutOff preserves
// the historical no-op behavior used by deployments without the overlay;
// shadow/reader explicitly reject direct callers so an internal integration
// cannot bypass the HTTP mutation gate during a staged rollout.
func (s *Service) mutationEnabled() (bool, error) {
	if s == nil || s.rollout == RolloutOff {
		return false, nil
	}
	if !s.WriterEnabled() {
		return false, ErrDisabled
	}
	return true, nil
}
