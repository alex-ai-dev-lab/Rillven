package service

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
)

// Matches official codex-api/rate_limits.rs. An observation needs a bounded
// percentage and a real reset; missing data stays unknown instead of unlimited.
func codexQuotaWindow(account, window string, used float64, reset, observed int64, source string) (model.AccountQuotaWindow, bool) {
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || reset <= observed || reset-observed > int64(31*24*time.Hour/time.Millisecond) {
		return model.AccountQuotaWindow{}, false
	}
	return model.AccountQuotaWindow{AccountID: account, Window: window, Remaining: 1 - math.Min(100, used)/100, ResetAt: reset, ObservedAt: observed, Source: source}, true
}

func ObserveCodexAccountQuota(ctx context.Context, info *relaycommon.RelayInfo, headers http.Header, payload []byte) {
	if info == nil || info.ChannelMeta == nil || channelIdentityScope(info) != "per_account" {
		return
	}
	scopes, err := resolveUpstreamScopes(info)
	if err != nil {
		return
	}
	var account model.UpstreamAccount
	if err := model.DB.WithContext(ctx).Where("subject_hash = ?", scopes.Account).First(&account).Error; err != nil {
		return
	}
	observed := info.UpstreamQuotaObservationAt
	if observed == 0 || gjson.GetBytes(payload, "type").String() == "codex.rate_limits" {
		observed, err = model.DatabaseMillis(ctx)
		if err != nil {
			return
		}
	}
	root := gjson.ParseBytes(payload)
	if len(payload) > 0 && root.Get("type").String() == "codex.rate_limits" {
		// Other metered limit families are model-specific, not the default pool.
		if name := root.Get("metered_limit_name").String(); name != "" && name != "codex" {
			return
		}
		if name := root.Get("limit_name").String(); name != "" && name != "codex" {
			return
		}
	}
	windows := make([]model.AccountQuotaWindow, 0, 2)
	for _, window := range []string{"primary", "secondary"} {
		used, parseErr := strconv.ParseFloat(headers.Get("x-codex-"+window+"-used-percent"), 64)
		reset, resetErr := strconv.ParseInt(headers.Get("x-codex-"+window+"-reset-at"), 10, 64)
		source := "headers"
		if len(payload) > 0 {
			value := root.Get("rate_limits." + window)
			source = "ws_event"
			if root.Get("type").String() != "codex.rate_limits" {
				value = root.Get("rate_limit." + window + "_window")
				source = "wham"
			}
			if !value.Get("used_percent").Exists() {
				continue
			}
			used, parseErr = strconv.ParseFloat(value.Get("used_percent").Raw, 64)
			reset, resetErr = strconv.ParseInt(value.Get("reset_at").Raw, 10, 64)
			if resetErr != nil && value.Get("reset_after_seconds").Exists() {
				seconds, err := strconv.ParseInt(value.Get("reset_after_seconds").Raw, 10, 64)
				if err == nil && seconds > 0 && seconds <= 31*24*3600 {
					reset, resetErr = observed/1000+seconds, nil
				}
			}
		}
		if parseErr != nil || resetErr != nil || reset > math.MaxInt64/1000 {
			continue
		}
		if value, ok := codexQuotaWindow(account.ID, window, used, reset*1000, observed, source); ok {
			windows = append(windows, value)
		}
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := model.StoreAccountQuota(persist, windows); err != nil {
		common.SysLog("account quota observation could not be persisted")
	}
}
