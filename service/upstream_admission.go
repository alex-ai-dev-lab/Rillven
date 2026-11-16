package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type upstreamScopes struct{ Account, Model, Egress string }

func resolveUpstreamScopes(info *relaycommon.RelayInfo) (upstreamScopes, error) {
	endpoint, err := url.Parse(info.ChannelBaseUrl)
	if err != nil {
		return upstreamScopes{}, err
	}
	subject, err := conversationCredentialID(info.ChannelType, info.ApiKey)
	if err != nil {
		return upstreamScopes{}, err
	}
	account := conversationHash([]byte(endpoint.Scheme + "/" + strings.ToLower(endpoint.Host) + "/" + subject))
	if channelIdentityScope(info) == "per_account" {
		provider, principal := "codex", info.ApiKey
		principal, err = upstreamAccountPrincipal(context.Background(), info.ChannelType, info.ApiKey, info.ChannelSetting)
		if err != nil {
			return upstreamScopes{}, err
		}
		if info.ChannelType == constant.ChannelTypeAnthropic {
			provider = "claude"
		}
		account = model.AccountSubjectHash(provider, principal)
	}
	return upstreamScopes{
		Account: account,
		Model:   conversationHash([]byte(account + "/model/" + info.UpstreamModel())),
		Egress:  conversationHash([]byte("egress/" + info.ChannelSetting.EgressPolicy + "/" + info.ChannelSetting.Proxy)),
	}, nil
}

func admissionError(err error) *types.NewAPIError {
	if errors.Is(err, model.ErrUpstreamAdmissionBusy) {
		return types.NewErrorWithStatusCode(errors.New("The upstream account is at capacity. Try again later."), types.ErrorCodeUpstreamAdmissionUnavailable, http.StatusTooManyRequests)
	}
	return types.NewErrorWithStatusCode(errors.New("Upstream request coordination is temporarily unavailable."), types.ErrorCodeUpstreamAdmissionUnavailable, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
}

// The SQL queue coordinates copies of the same account across gateway instances.
// Only opted-in channels pay its storage cost. Existing token/user limits remain.
func WithUpstreamAdmission(c *gin.Context, info *relaycommon.RelayInfo, call func() *types.NewAPIError) *types.NewAPIError {
	if common.GetContextKeyInt(c, constant.ContextKeyChannelId) > 0 {
		info.InitChannelMeta(c)
	}
	settings, _ := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
	if !settings.UpstreamCoordination && settings.ClientIdentityMode != "per_account" && channelIdentityScope(info) != "per_account" {
		return call()
	}
	scopes, err := resolveUpstreamScopes(info)
	if err != nil {
		return admissionError(err)
	}
	scopeList := []string{scopes.Account, scopes.Model}
	if settings.Proxy != "" {
		scopeList = append(scopeList, scopes.Egress)
	}
	until, err := model.UpstreamBlockedUntil(c.Request.Context(), scopeList)
	if err != nil {
		return admissionError(err)
	}
	if until > 0 {
		return admissionError(model.ErrUpstreamAdmissionBusy)
	}
	limit := max(1, min(common.GetEnvOrDefault("RILLVEN_UPSTREAM_CONCURRENCY", 4), 64))
	if channelIdentityScope(info) == "per_account" {
		halfOpen, err := model.AccountQuotaHalfOpen(c.Request.Context(), scopes.Account)
		if err != nil {
			return admissionError(err)
		}
		if halfOpen {
			limit = 1
		}
	}
	queueLimit := max(1, min(common.GetEnvOrDefault("RILLVEN_UPSTREAM_QUEUE", 32), 256))
	wait := time.Duration(max(100, min(common.GetEnvOrDefault("RILLVEN_UPSTREAM_WAIT_MS", 3000), 30000))) * time.Millisecond
	row, err := model.QueueUpstreamAdmission(c.Request.Context(), scopes.Account, uuid.NewString(), queueLimit, wait)
	if err != nil {
		return admissionError(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
		defer cancel()
		_ = model.ReleaseUpstreamAdmission(ctx, row)
	}()
	waitCtx, waitCancel := context.WithTimeout(c.Request.Context(), wait)
	defer waitCancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	const lease = 45 * time.Second
	for {
		active, err := model.TryActivateUpstreamAdmission(waitCtx, row, limit, lease)
		if err != nil {
			return admissionError(err)
		}
		if active {
			break
		}
		select {
		case <-waitCtx.Done():
			return admissionError(model.ErrUpstreamAdmissionBusy)
		case <-ticker.C:
		}
	}
	// A queued request must recheck newer model/egress cooldowns before dispatch.
	until, err = model.UpstreamBlockedUntil(c.Request.Context(), scopeList)
	if err != nil {
		return admissionError(err)
	}
	if until > 0 {
		return admissionError(model.ErrUpstreamAdmissionBusy)
	}
	if info.ChannelConfigVersion > 0 {
		var count int64
		if err := model.DB.WithContext(c.Request.Context()).Model(&model.Channel{}).Where("id = ? AND config_version = ? AND status = ?", info.ChannelId, info.ChannelConfigVersion, common.ChannelStatusEnabled).Count(&count).Error; err != nil {
			return admissionError(err)
		}
		if count != 1 {
			return admissionError(model.ErrUpstreamAdmissionBusy)
		}
	}
	request := c.Request
	ctx, cancel := context.WithCancelCause(request.Context())
	c.Request = request.WithContext(ctx)
	done := make(chan struct{})
	var worker sync.WaitGroup
	worker.Add(1)
	go func() {
		defer worker.Done()
		renew := time.NewTicker(10 * time.Second)
		defer renew.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-renew.C:
				check, finish := context.WithTimeout(ctx, 3*time.Second)
				err := model.RenewUpstreamAdmission(check, row, lease)
				finish()
				if err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	defer func() { close(done); cancel(nil); worker.Wait(); c.Request = request }()
	result := call()
	if context.Cause(ctx) != nil && request.Context().Err() == nil && result != nil {
		return admissionError(context.Cause(ctx))
	}
	if result != nil && !types.IsLocalRelayRejection(result) {
		now, clockErr := model.DatabaseMillis(context.WithoutCancel(ctx))
		if clockErr != nil {
			return result
		}
		deadline := now + max(RouterCooldownTTL(), min(result.RetryHint, 24*time.Hour)).Milliseconds()
		scope := ""
		switch result.StatusCode {
		case http.StatusTooManyRequests:
			scope = scopes.Model
			if UsesAccountIdentity(info) {
				scope = scopes.Account
			}
		case http.StatusUnauthorized:
			scope = scopes.Account
			deadline = now + time.Hour.Milliseconds()
		case http.StatusProxyAuthRequired:
			if settings.Proxy != "" {
				scope = scopes.Egress
			}
		}
		if scope != "" {
			persist, finish := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_ = model.BlockUpstreamScope(persist, scope, deadline)
			finish()
		}
	}
	return result
}

// Accept reset headers only together with an explicit exhausted counter.
// Successful older calls never clear a newer quota window.
func ObserveUpstreamQuotaHeaders(ctx context.Context, info *relaycommon.RelayInfo, headers http.Header) {
	ObserveCodexAccountQuota(ctx, info, headers, nil)
	if info == nil || info.ChannelMeta == nil || !info.ChannelSetting.UpstreamCoordination && !UsesAccountIdentity(info) {
		return
	}
	scopes, err := resolveUpstreamScopes(info)
	if err != nil {
		return
	}
	nowMillis, clockErr := model.DatabaseMillis(ctx)
	if clockErr != nil {
		return
	}
	now := time.UnixMilli(nowMillis)
	var until time.Time
	for _, resource := range []string{"requests", "tokens"} {
		remaining, err := strconv.ParseInt(headers.Get("x-ratelimit-remaining-"+resource), 10, 64)
		if err != nil || remaining > 0 {
			continue
		}
		value := headers.Get("x-ratelimit-reset-" + resource)
		if value == "" {
			continue
		}
		var reset time.Time
		if duration, err := time.ParseDuration(value); err == nil {
			reset = now.Add(duration)
		} else if epoch, err := strconv.ParseInt(value, 10, 64); err == nil {
			reset = time.Unix(epoch, 0)
		}
		if reset.After(until) {
			until = reset
		}
	}
	if until.After(now) {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		scope := scopes.Model
		if UsesAccountIdentity(info) {
			scope = scopes.Account
		}
		if err := model.BlockUpstreamScope(persist, scope, until.UnixMilli()); err != nil {
			common.SysLog("upstream quota window persistence failed")
		}
	}
}
