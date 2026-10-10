package auth

import (
	"context"
	"errors"
	"strings"

	"backend/internal/database"
)

type Kind string

const (
	KindUser    Kind = "user"
	KindAccount Kind = "account"
)

type Identity struct {
	Kind      Kind
	AccountID string
	Subject   string
	UserName  string
}

func (i Identity) IsUser() bool { return i.Kind == KindUser }

func (i Identity) Owns(accountID string) bool {
	return i.Kind == KindAccount && i.AccountID == accountID
}

var (
	ErrNoToken      = errors.New("未提供 token")
	ErrInvalidToken = errors.New("token 無效")
)

type Settings struct {
	UserName             string
	StaticUserToken      string
	AllowStaticUserToken bool
	OIDC                 *OIDCSettings
}

// Authenticator keeps OIDC USER and opaque Account-token authentication as
// separate paths. A JWT-shaped token never falls back after OIDC rejection.
type Authenticator struct {
	settings Settings
	oidc     *OIDCVerifier
	store    *database.Store
}

func New(settings Settings, store *database.Store) *Authenticator {
	var verifier *OIDCVerifier
	if settings.OIDC != nil {
		verifier = NewOIDCVerifier(*settings.OIDC)
	}
	return &Authenticator{settings: settings, oidc: verifier, store: store}
}

func (a *Authenticator) Resolve(ctx context.Context, raw string) (Identity, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return Identity{}, ErrNoToken
	}

	if strings.Count(token, ".") == 2 {
		if a.oidc == nil {
			return Identity{}, ErrInvalidToken
		}
		subject, err := a.oidc.Verify(ctx, token)
		if err != nil {
			return Identity{}, ErrInvalidToken
		}
		return Identity{Kind: KindUser, Subject: subject, UserName: a.settings.UserName}, nil
	}

	if a.settings.AllowStaticUserToken && token == a.settings.StaticUserToken {
		return Identity{Kind: KindUser, UserName: a.settings.UserName}, nil
	}

	acc, err := a.store.AccountByToken(ctx, token)
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return Identity{}, ErrInvalidToken
		}
		return Identity{}, err
	}
	if !acc.Active() {
		return Identity{}, ErrInvalidToken
	}

	return Identity{Kind: KindAccount, AccountID: acc.ID, UserName: acc.UserName}, nil
}

func BearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	if len(header) >= 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return header
}
