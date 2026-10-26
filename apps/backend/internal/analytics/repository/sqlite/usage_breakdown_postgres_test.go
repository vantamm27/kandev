package sqlite

// PostgreSQL parity for GetUsageBreakdown: the day bucket uses dialect.DateText
// and the grouping is positional because PostgreSQL resolves a bare GROUP BY
// name that matches an input column to that column, not the select alias.
// Skips unless KANDEV_TEST_POSTGRES_DSN is set.

import (
	"testing"

	"github.com/kandev/kandev/internal/testutil"
)

func TestPostgresGetUsageBreakdownGroupsByDayExecutorAssigneeAndModel(t *testing.T) {
	db := testutil.OpenIsolatedPostgres(t, testutil.PostgresDSNFromEnv(t))
	assertUsageBreakdownGrouping(t, newUsageFixtureOn(t, db))
}
