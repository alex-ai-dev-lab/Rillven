package service

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

func withCodexRefreshJournal(ctx context.Context, grant string, refresh func() (*CodexOAuthTokenResult, error), beforeSend ...func() error) (*CodexOAuthTokenResult, error) {
	cipher, err := common.RelayStateCipher()
	if err != nil {
		return nil, err
	}
	hash, owner := conversationHash([]byte("codex-refresh/"+grant)), uuid.NewString()
	row, err := model.ClaimCodexRefresh(ctx, hash, owner)
	if err != nil {
		return nil, err
	}
	for _, check := range beforeSend {
		if err := check(); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = model.ReleaseUnsentCodexRefresh(cleanup, hash, owner)
			return nil, err
		}
	}
	if row.State == "received" {
		plain, err := cipher.Open("codex-refresh/"+hash, string(row.Ciphertext))
		if err != nil {
			return nil, err
		}
		var result CodexOAuthTokenResult
		if err := common.Unmarshal(plain, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}
	if err := model.MarkCodexRefreshSent(ctx, hash, owner); err != nil {
		return nil, err
	}
	result, callErr := refresh()
	// A cancelled caller cannot discard a successful token rotation. Save using
	// a separate short deadline before attempting to modify any channel row.
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if callErr != nil {
		state := "unknown"
		var oauthErr *CodexOAuthError
		if errors.As(callErr, &oauthErr) && oauthErr.NeedsLogin {
			state = "needs_login"
		}
		if err := model.RecordCodexRefreshResult(persist, hash, owner, state, ""); err != nil {
			return nil, err
		}
		return nil, callErr
	}
	plain, err := common.Marshal(result)
	if err != nil {
		return nil, err
	}
	encrypted, err := cipher.Seal("codex-refresh/"+hash, plain)
	if err != nil {
		return nil, err
	}
	if err := model.RecordCodexRefreshResult(persist, hash, owner, "received", encrypted); err != nil {
		return nil, err
	}
	return result, nil
}

type CodexOAuthError struct {
	Status     int
	NeedsLogin bool
}

func (e *CodexOAuthError) Error() string {
	if e.NeedsLogin {
		return "OAuth authorization is no longer valid; sign in again"
	}
	return "OAuth refresh was rejected; the saved grant must not be replayed automatically"
}
