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

// Policy is how an action behaves under the user's configuration.
type Policy struct {
	Confirm     bool
	AllowRemote bool
}

// PolicyFor resolves an action's configured policy over its defaults: destructive actions ask
// for confirmation, and peers accept only actions that are remote by default.
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

// ValidateConfig rejects [actions] entries that name no registered action or allow remote use
// of an action that cannot run on a peer, and key bindings that cannot run their action.
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
