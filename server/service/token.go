package service

import (
    "context"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "strings"
    "time"

    "server/domain"
    "server/model/repo"
    "server/model/store"
)

type TokenService struct {
    repo   *repo.Repository
    clock  domain.Clock
    events domain.EventPublisher
}

type CreateTokenInput struct {
    AccountID string     `json:"account_id"`
    Name      string     `json:"name"`
    Scopes    []string   `json:"scopes"`
    ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func NewTokenService(repo *repo.Repository, clock domain.Clock, events domain.EventPublisher) *TokenService {
    return &TokenService{repo: repo, clock: clock, events: events}
}

func (s *TokenService) Create(ctx context.Context, input CreateTokenInput) (string, *store.AccountToken, error) {
    if input.AccountID == "" {
        return "", nil, domain.ValidationError("INVALID_ACCOUNT_ID", "account_id is required")
    }
    if _, err := s.repo.FindAccount(ctx, input.AccountID); err != nil {
        return "", nil, domain.NotFoundError("ACCOUNT_NOT_FOUND", "account not found")
    }
    if len(input.Scopes) == 0 {
        return "", nil, domain.ValidationError("INVALID_SCOPE", "at least one scope is required")
    }
    tokenID := newID("tok")
    secret := newID("sec")
    plaintext := tokenID + "." + secret
    record := &store.AccountToken{
        ID:        tokenID,
        AccountID: input.AccountID,
        TokenName: input.Name,
        TokenHash: hashTokenSecret(secret),
        Scope:     strings.Join(uniqueStrings(input.Scopes), ","),
        ExpiresAt: input.ExpiresAt,
    }
    if err := s.repo.Create(ctx, record); err != nil {
        return "", nil, err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.token.created", AccountID: input.AccountID, AggregateID: tokenID, Payload: map[string]any{"account_id": input.AccountID}})
    return plaintext, record, nil
}

func (s *TokenService) Rotate(ctx context.Context, tokenID string) (string, *store.AccountToken, error) {
    record, err := s.repo.FindToken(ctx, tokenID)
    if err != nil {
        return "", nil, domain.NotFoundError("TOKEN_NOT_FOUND", "token not found")
    }
    secret := newID("sec")
    record.TokenHash = hashTokenSecret(secret)
    record.RevokedAt = nil
    if err := s.repo.Save(ctx, record); err != nil {
        return "", nil, err
    }
    plaintext := record.ID + "." + secret
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.token.rotated", AccountID: record.AccountID, AggregateID: record.ID, Payload: map[string]any{"account_id": record.AccountID}})
    return plaintext, record, nil
}

func (s *TokenService) Revoke(ctx context.Context, tokenID string) error {
    record, err := s.repo.FindToken(ctx, tokenID)
    if err != nil {
        return domain.NotFoundError("TOKEN_NOT_FOUND", "token not found")
    }
    now := s.clock.Now()
    record.RevokedAt = &now
    if err := s.repo.Save(ctx, record); err != nil {
        return err
    }
    _ = s.events.Publish(ctx, domain.DomainEvent{Topic: "account.token.revoked", AccountID: record.AccountID, AggregateID: record.ID, Payload: map[string]any{"account_id": record.AccountID}})
    return nil
}

func (s *TokenService) Verify(ctx context.Context, rawToken string, requiredScopes []string) (*store.AccountToken, *store.Account, error) {
    tokenID, secret, err := splitRawToken(rawToken)
    if err != nil {
        return nil, nil, domain.UnauthorizedError("invalid token")
    }
    record, err := s.repo.FindToken(ctx, tokenID)
    if err != nil {
        return nil, nil, domain.UnauthorizedError("token not found")
    }
    if record.RevokedAt != nil {
        return nil, nil, domain.UnauthorizedError("token revoked")
    }
    if record.ExpiresAt != nil && record.ExpiresAt.Before(s.clock.Now()) {
        return nil, nil, domain.UnauthorizedError("token expired")
    }
    if subtle.ConstantTimeCompare([]byte(record.TokenHash), []byte(hashTokenSecret(secret))) != 1 {
        return nil, nil, domain.UnauthorizedError("invalid token")
    }
    scopes := map[string]struct{}{}
    for _, scope := range record.Scopes() {
        scopes[scope] = struct{}{}
    }
    for _, required := range requiredScopes {
        if _, ok := scopes[required]; ok {
            continue
        }
        return nil, nil, domain.ForbiddenError("token scope is not sufficient")
    }
    account, err := s.repo.FindAccount(ctx, record.AccountID)
    if err != nil {
        return nil, nil, domain.UnauthorizedError("account not found")
    }
    if account.Status != store.AccountStatusActive {
        return nil, nil, domain.ForbiddenError("account is disabled")
    }
    now := s.clock.Now()
    record.LastUsedAt = &now
    _ = s.repo.Save(ctx, record)
    return record, account, nil
}

func hashTokenSecret(secret string) string {
    sum := sha256.Sum256([]byte(secret))
    return hex.EncodeToString(sum[:])
}

func splitRawToken(raw string) (string, string, error) {
    token := strings.TrimSpace(raw)
    parts := strings.Split(token, ".")
    if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
        return "", "", domain.ValidationError("INVALID_TOKEN", "invalid token format")
    }
    return parts[0], parts[1], nil
}

func uniqueStrings(items []string) []string {
    seen := map[string]struct{}{}
    out := make([]string, 0, len(items))
    for _, item := range items {
        trimmed := strings.TrimSpace(item)
        if trimmed == "" {
            continue
        }
        if _, ok := seen[trimmed]; ok {
            continue
        }
        seen[trimmed] = struct{}{}
        out = append(out, trimmed)
    }
    return out
}
