package action

import (
	"errors"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

var (
	ErrUnknownConfiguredAction = errors.New("configuration names an unknown action")
	ErrRemoteNotSupported      = errors.New("configuration allows remote use of an action that cannot run remotely")
)

type Policy struct {
	Confirm     bool
	AllowRemote bool
}

// PolicyFor defaults to confirming destructive actions and letting peers run only actions that
// are remote by default; configuration overrides both, except for RemoteNever actions.
func PolicyFor(cfg *config.Config, descriptor Descriptor) Policy {
	policy := Policy{Confirm: descriptor.Destructive, AllowRemote: descriptor.Remote == RemoteOnByDefault}

	var override config.ActionConfig
	if cfg != nil {
		override = cfg.Actions[descriptor.ID.Group()][descriptor.ID.Verb()]
	}

	if override.Confirm != nil {
		policy.Confirm = *override.Confirm
	}

	if override.AllowRemote != nil {
		policy.AllowRemote = *override.AllowRemote
	}

	if descriptor.Remote == RemoteNever {
		policy.AllowRemote = false
	}

	return policy
}

func ValidateConfig(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	for group, verbs := range cfg.Actions {
		for verb, override := range verbs {
			entry, ok := Lookup(ID(group + "." + verb))
			if !ok {
				return oops.In("action").Code("config_invalid").With("operation", "validate_config", "reason", "unknown_action").
					Wrap(ErrUnknownConfiguredAction)
			}

			if override.AllowRemote != nil && *override.AllowRemote && entry.Describe().Remote == RemoteNever {
				return oops.In("action").Code("config_invalid").With("operation", "validate_config", "reason", "remote_not_supported").
					Wrap(ErrRemoteNotSupported)
			}
		}
	}

	_, err := Bindings(cfg)

	return err
}
