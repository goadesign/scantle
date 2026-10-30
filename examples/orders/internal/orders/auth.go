package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	genorders "github.com/goadesign/scantle/examples/orders/gen/orders"
	"github.com/golang-jwt/jwt/v5"
	"goa.design/goa/v3/security"
)

type (
	// Principal is the authenticated caller of the public orders API.
	Principal struct {
		// TenantID is the merchant tenant the caller belongs to.
		TenantID string
		// Subject identifies the caller: the customer ID for customers, the
		// staff member ID for managers.
		Subject string
		// Scopes are the scopes granted to the caller.
		Scopes []string
	}

	// claims is the JWT body issued by the merchant's identity provider.
	claims struct {
		jwt.RegisteredClaims
		// TenantID is the merchant tenant the token belongs to.
		TenantID string `json:"tenant_id"`
		// Scope lists the granted scopes separated by spaces.
		Scope string `json:"scope"`
	}

	// principalKey keys the authenticated Principal in the request context.
	principalKey struct{}
)

// JWTAuth verifies the bearer token signature and expiry, then checks that it
// grants every scope the called method requires. A bad token yields the
// unauthorized error (401); a valid token missing a scope yields forbidden
// (403). The principal is stored in the returned context.
func (s *Service) JWTAuth(ctx context.Context, token string, scheme *security.JWTScheme) (context.Context, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return s.signingKey, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return ctx, genorders.MakeUnauthorized(fmt.Errorf("invalid bearer token: %w", err))
	}
	if c.TenantID == "" || c.Subject == "" {
		return ctx, genorders.MakeUnauthorized(errors.New("bearer token must name a tenant and a subject"))
	}
	p := Principal{TenantID: c.TenantID, Subject: c.Subject, Scopes: strings.Fields(c.Scope)}
	if err := scheme.Validate(p.Scopes); err != nil {
		return ctx, genorders.MakeForbidden(err)
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// principal returns the caller authenticated by JWTAuth. Generated endpoints
// always run JWTAuth before secured methods, so a missing principal is a
// wiring bug.
func principal(ctx context.Context) Principal {
	return ctx.Value(principalKey{}).(Principal)
}
