package controlplane

import (
	"context"
	"errors"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// errVerificationUnavailable is returned by the CP4 verification methods
// until a control plane is given a verification configuration.
var errVerificationUnavailable = errors.New("verification is not configured on this control plane")

// VerifyChange starts Checkpoint 4 verification of a candidate commit. It is
// not available yet: every call fails with errVerificationUnavailable.
func (c *ControlPlane) VerifyChange(context.Context, *gensurla.VerifyChangePayload) (*gensurla.VerificationStatusResult, error) {
	return nil, errVerificationUnavailable
}

// GetVerification returns the state of a verification run. It is not
// available yet: every call fails with errVerificationUnavailable.
func (c *ControlPlane) GetVerification(context.Context, *gensurla.GetVerificationPayload) (*gensurla.VerificationStatusResult, error) {
	return nil, errVerificationUnavailable
}
