package errorstep

import "github.com/samber/oops"

type Step struct {
	Operation string
	Reason    string
}

// Owned returns the deepest step domain recorded in err, and false when it recorded none. In
// joined errors only the first branch holding an oops error is followed, so a primary failure
// wins over cleanup failures joined after it.
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
