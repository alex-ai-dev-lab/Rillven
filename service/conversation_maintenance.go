package service

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func RunConversationMaintenance(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			work, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := model.ReconcileConversationCommits(work, 64)
			if err == nil {
				err = model.CleanupConversationState(work, 32)
			}
			if err == nil {
				err = model.CompactCodexRefreshReceipts(work)
			}
			if err == nil {
				err = model.CleanupUpstreamAdmissions(work)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				common.SysLog("conversation maintenance failed")
			}
		}
	}
}
