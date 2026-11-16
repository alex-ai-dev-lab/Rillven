package service

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

func recordBillingReconcileFailure(id uint64, desired string, quota int64, cause error, channel ...int) error {
	err := model.MarkBillingLedgerForReconcile(id, desired, quota, cause, channel...)
	if err == nil {
		return cause
	}
	common.SysLog(fmt.Sprintf("billing reconciliation intent persistence failed: ledger_id=%d desired=%s quota=%d", id, desired, quota))
	return errors.Join(cause, fmt.Errorf("billing reconciliation intent could not be persisted: %w", err))
}
