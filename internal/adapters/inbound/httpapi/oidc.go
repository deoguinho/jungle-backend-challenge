package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/junglegaming/backend-challenge-go/internal/application"
)

type Authenticator struct {
	verifier         *oidc.IDTokenVerifier
	internalClientID string
	providerClientID string
	providerID       string
}

func NewAuthenticator(ctx context.Context, issuer, internalClientID, providerClientID, providerID string) (*Authenticator, error) {
	if issuer == "" || internalClientID == "" || providerClientID == "" || providerID == "" {
		return nil, errors.New("OIDC issuer, client IDs and provider ID are required")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}
	return &Authenticator{verifier: provider.Verifier(&oidc.Config{ClientID: internalClientID, SkipClientIDCheck: true}), internalClientID: internalClientID, providerClientID: providerClientID, providerID: providerID}, nil
}

func (a *Authenticator) Principal(ctx context.Context, authorization string) (application.Principal, error) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return application.Principal{}, application.ErrUnauthorized
	}
	token, err := a.verifier.Verify(ctx, parts[1])
	if err != nil {
		return application.Principal{}, fmt.Errorf("%w: invalid bearer token", application.ErrUnauthorized)
	}
	var claims map[string]any
	if err = token.Claims(&claims); err != nil {
		return application.Principal{}, fmt.Errorf("%w: invalid token claims", application.ErrUnauthorized)
	}
	client := stringClaim(claims, "azp")
	if client == "" {
		client = stringClaim(claims, "client_id")
	}
	switch client {
	case a.internalClientID:
		return application.Principal{Internal: true}, nil
	case a.providerClientID:
		providerID := stringClaim(claims, "provider_id")
		if providerID == "" {
			providerID = providerRole(claims, a.providerClientID)
		}
		if providerID == "" {
			providerID = a.providerID
		}
		return application.Principal{ProviderID: providerID, PlayerID: stringClaim(claims, "player_id")}, nil
	default:
		return application.Principal{}, application.ErrUnauthorized
	}
}

func stringClaim(claims map[string]any, key string) string {
	value, _ := claims[key].(string)
	return value
}
func providerRole(claims map[string]any, clientID string) string {
	resource, _ := claims["resource_access"].(map[string]any)
	client, _ := resource[clientID].(map[string]any)
	roles, _ := client["roles"].([]any)
	for _, value := range roles {
		role, _ := value.(string)
		if strings.HasPrefix(role, "provider:") {
			return strings.TrimPrefix(role, "provider:")
		}
	}
	return ""
}
