package admin

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func usageSpeedContext(values url.Values) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/admin/usage/speed?"+values.Encode(), nil)
	return c
}

func TestUsageSpeedQueryKeepsUsageDetailFilters(t *testing.T) {
	q, err := parseUsageSpeedQuery(usageSpeedContext(url.Values{
		"start_date": {"2025-01-01"}, "end_date": {"2025-12-31"}, "timezone": {"Asia/Shanghai"},
		"user_id": {"3"}, "api_key_id": {"7"}, "account_id": {"9"}, "group_id": {"2"},
		"model": {"requested-model"}, "request_type": {"stream"}, "stream": {"false"},
		"billing_type": {"1"}, "billing_mode": {"token"}, "native_compaction_v2": {"true"}, "upstream_model_mismatch": {"false"},
		"analysis": {"true"}, "comparison": {"week"}, "source": {"probe"}, "task_id": {"99"}, "profile": {"legacy"}, "status": {"success"},
	}), time.Now())
	require.NoError(t, err, "usage analysis supports the same calendar ranges as usage records")
	require.Equal(t, "real", q.Source)
	require.Equal(t, "requested-model", q.Model)
	require.Zero(t, q.TaskID)
	require.Empty(t, q.Profile)
	require.Empty(t, q.Status, "benchmark statuses do not apply to recorded usage")
	require.Equal(t, "all", q.Stream, "request_type takes precedence over the legacy stream flag")
	require.True(t, q.Analysis)
	require.Equal(t, "week", q.Comparison)
	f := q.UsageFilters
	require.NotNil(t, f)
	require.EqualValues(t, 3, f.UserID)
	require.EqualValues(t, 7, f.APIKeyID)
	require.EqualValues(t, 9, f.AccountID)
	require.EqualValues(t, 2, f.GroupID)
	require.Equal(t, int16(service.RequestTypeStream), *f.RequestType)
	require.EqualValues(t, 1, *f.BillingType)
	require.Equal(t, "token", f.BillingMode)
	require.True(t, *f.NativeCompactionV2)
	require.False(t, *f.UpstreamModelMismatch)
}

func TestUsageSpeedQueryRejectsUnsupportedMetricsAndMalformedFilters(t *testing.T) {
	for _, values := range []url.Values{
		{"metric": {"total_ms"}}, {"metric": {"first_output_ms"}}, {"user_id": {"bad"}},
		{"request_type": {"bad"}}, {"native_compaction_v2": {"maybe"}}, {"billing_type": {"999"}},
	} {
		_, err := parseUsageSpeedQuery(usageSpeedContext(values), time.Now())
		require.Error(t, err)
	}
}
