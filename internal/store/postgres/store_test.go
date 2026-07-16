package postgres

import (
	"context"
	"testing"

	"github.com/goalforge/goalforge/internal/scheduler"
)

func TestStoreImplementsDistributedSchedulerContract(t *testing.T) {
	var _ scheduler.JobStore = (*Store)(nil)
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("accepted empty PostgreSQL DSN")
	}
}
