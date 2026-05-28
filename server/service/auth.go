package service

import (
	"context"

	"server/domain"
	"server/model/repo"
	"server/model/store"

	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	repo   *repo.Repository
	tokens *TokenService
	clock  domain.Clock
}

func NewAuthService(repo *repo.Repository, tokens *TokenService, clock domain.Clock) *AuthService {
	return &AuthService{repo: repo, tokens: tokens, clock: clock}
}

func (s *AuthService) LoginAdmin(ctx context.Context, username, password string) (*store.User, error) {
	if username == "" || password == "" {
		return nil, domain.ValidationError("INVALID_CREDENTIALS", "username and password are required")
	}
	user, err := s.repo.FindUserByUsername(ctx, username)
	if err != nil {
		return nil, domain.UnauthorizedError("invalid credentials")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password+user.Salt)) != nil {
		return nil, domain.UnauthorizedError("invalid credentials")
	}
	if user.Role != store.RoleRootUser {
		return nil, domain.ForbiddenError("admin role required")
	}
	return user, nil
}

func (s *AuthService) LoginToken(ctx context.Context, rawToken string) (*store.AccountToken, *store.Account, error) {
	return s.tokens.Verify(ctx, rawToken, nil)
}
