// Package errorstep reads the step a domain recorded on an error it owns, so a boundary can log
// that domain's own fixed operation and reason instead of arbitrary cause details.
package errorstep

import "github.com/samber/oops"

// Step is the operation and optional reason a domain recorded when one of its steps failed.
type Step struct {
	Operation string
	Reason    string
}

// Owned returns the deepest step recorded by the given oops domain in err. Inside joined errors
// only the first branch holding an oops error is followed, so a primary failure wins over the
// cleanup failures joined after it. It reports false when that domain recorded no operation.
func Owned(err error, domain string) (Step, bool) {
	var found Step

	diagnostic, ok := oops.AsOops(err)
	if !ok {
		return found, false
	}

	for _, layer := range diagnostic.Layers() {
		operation, _ := layer.Context["operation"].(string)
		if layer.Domain != domain || operation == "" {
			continue
		}

		reason, _ := layer.Context["reason"].(string)
		found = Step{Operation: operation, Reason: reason}
	}

	return found, found.Operation != ""
}
