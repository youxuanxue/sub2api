package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLooksLikeSystemKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"sk-abcdef0123456789", true},
		{"ABCdef_-0123456789", true},
		{"short", false},
		{"with space xxxxxxxxxx", false},
		{"汉字key1234567890", false},
		{"", false},
	}
	for _, c := range cases {
		if got := looksLikeSystemKey(c.in); got != c.want {
			t.Errorf("looksLikeSystemKey(%q)=%v want %v", c.in, got, c.want)
		}
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if looksLikeSystemKey(string(long)) {
		t.Errorf("129-char key should be rejected")
	}
}

func TestKeyPrefix(t *testing.T) {
	if got := keyPrefix("sk-3f2a9c7e", 8); got != "sk-3f2a9" {
		t.Errorf("keyPrefix=%q want %q", got, "sk-3f2a9")
	}
	if got := keyPrefix("abc", 8); got != "abc" {
		t.Errorf("short key should be returned as-is, got %q", got)
	}
}

func TestExtractAttemptedKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "Bearer in Authorization",
			headers: map[string]string{"Authorization": "Bearer sk-testkey0123456789"},
			want:    "sk-testkey0123456789",
		},
		{
			name:    "Bearer case-insensitive",
			headers: map[string]string{"Authorization": "BEARER sk-testkey0123456789"},
			want:    "sk-testkey0123456789",
		},
		{
			name:    "x-api-key header",
			headers: map[string]string{"x-api-key": "sk-xapikey0123456789"},
			want:    "sk-xapikey0123456789",
		},
		{
			name:    "x-goog-api-key header",
			headers: map[string]string{"x-goog-api-key": "sk-goog0123456789"},
			want:    "sk-goog0123456789",
		},
		{
			name:    "Authorization takes priority over x-api-key",
			headers: map[string]string{"Authorization": "Bearer sk-auth0123456789", "x-api-key": "sk-xapi0123456789"},
			want:    "sk-auth0123456789",
		},
		{
			name:    "x-api-key takes priority over x-goog-api-key",
			headers: map[string]string{"x-api-key": "sk-xapi0123456789", "x-goog-api-key": "sk-goog0123456789"},
			want:    "sk-xapi0123456789",
		},
		{
			name:    "no key headers",
			headers: map[string]string{},
			want:    "",
		},
		{
			name:    "Bearer with leading/trailing spaces trimmed",
			headers: map[string]string{"Authorization": "Bearer   sk-trimmed0123456789  "},
			want:    "sk-trimmed0123456789",
		},
		{
			// 非 Bearer Authorization 应被忽略,继续 fall-through 到 x-api-key(与认证中间件一致)
			name:    "non-Bearer Authorization falls through to x-api-key",
			headers: map[string]string{"Authorization": "junk-not-bearer", "x-api-key": "sk-realkey1234567"},
			want:    "sk-realkey1234567",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			c.Request = req

			got := extractAttemptedKey(c)
			if got != tc.want {
				t.Errorf("extractAttemptedKey(%v) = %q, want %q", tc.headers, got, tc.want)
			}
		})
	}
}

// deletedKeyAuditOpsRepo 让 LookupDeletedKeyAudit 可控:命中返回 result,未命中返回
// (nil, nil),并记录被查的明文 key,用来断言「非系统形状的 key 根本不查审计表」。
type deletedKeyAuditOpsRepo struct {
	service.OpsRepository
	result    *service.DeletedKeyAuditResult
	lookupErr error
	lookedUp  []string
}

func (r *deletedKeyAuditOpsRepo) LookupDeletedKeyAudit(_ context.Context, key string) (*service.DeletedKeyAuditResult, error) {
	r.lookedUp = append(r.lookedUp, key)
	if r.lookupErr != nil {
		return nil, r.lookupErr
	}
	return r.result, nil
}

func (r *deletedKeyAuditOpsRepo) InsertErrorLog(_ context.Context, _ *service.OpsInsertErrorLogInput) (int64, error) {
	return 0, nil
}

// TestOpsErrorLoggerMiddlewareWritesDeletedKeyAttribution 是本文件里唯一覆盖「写入方真
// 的把三个列填上」的测试,其余用例只测 helper 函数。
//
// 这三列(attempted_key_prefix / deleted_key_owner_user_id / deleted_key_name)由迁移
// 145 声明,写入方只有中间件的 INVALID_API_KEY 分支这一处。它曾被一次上游合并静默删掉:
// 代码照常编译、helper 的单测照常全绿,只有读取侧(ops_repo_user_visible_failure_tk.go
// 的 SLA 分子)悄悄归因不到任何用户。helper 级断言看不见这种丢失,sentinel 钉的是字面量、
// 只拦「改回原样」,所以这里从中间件外部驱动一次真实请求,断言落库 entry 上的字段。
func TestOpsErrorLoggerMiddlewareWritesDeletedKeyAttribution(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	repo := &deletedKeyAuditOpsRepo{result: &service.DeletedKeyAuditResult{UserID: 4242, KeyName: "retired-key"}}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "INVALID_API_KEY", "message": "Invalid API key"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-deleted-key-0123456789")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	entry := (<-opsErrorLogQueue).entry

	require.Equal(t, "sk-delet", entry.AttemptedKeyPrefix,
		"attempted_key_prefix 必须落前 8 位,否则 ops 详情页看不到试过的是哪个 key")
	require.NotNil(t, entry.DeletedKeyOwnerUserID,
		"deleted_key_owner_user_id 没写入:已删除 key 上的失败会从 SLA 分子里消失")
	require.Equal(t, int64(4242), *entry.DeletedKeyOwnerUserID)
	require.Equal(t, "retired-key", entry.DeletedKeyName)
	require.Equal(t, []string{"sk-deleted-key-0123456789"}, repo.lookedUp)
}

// 审计未命中(key 从未被本系统删除过)时,仍要留下 attempted_key_prefix 供排查,但不能
// 凭空造出一个所有者。
func TestOpsErrorLoggerMiddlewareAttributionMissDoesNotInventAnOwner(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	repo := &deletedKeyAuditOpsRepo{result: nil}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "INVALID_API_KEY", "message": "Invalid API key"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "sk-never-existed-01234")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	entry := (<-opsErrorLogQueue).entry

	require.Equal(t, "sk-never", entry.AttemptedKeyPrefix)
	require.Nil(t, entry.DeletedKeyOwnerUserID)
	require.Empty(t, entry.DeletedKeyName)
}

// 非系统形状的输入(随机扫描、乱码)不得拿去查审计表:looksLikeSystemKey 是那道粗筛,
// 但前缀仍要记录,因为「有人拿垃圾串来试」本身是排查信息。
func TestOpsErrorLoggerMiddlewareSkipsAuditLookupForNonSystemKey(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	repo := &deletedKeyAuditOpsRepo{result: &service.DeletedKeyAuditResult{UserID: 7, KeyName: "should-not-be-read"}}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(http.StatusUnauthorized, gin.H{"code": "INVALID_API_KEY", "message": "Invalid API key"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", "not a key at all")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	entry := (<-opsErrorLogQueue).entry

	require.Equal(t, "not a ke", entry.AttemptedKeyPrefix)
	require.Nil(t, entry.DeletedKeyOwnerUserID)
	require.Empty(t, repo.lookedUp, "非系统形状的 key 不该触发审计表查询")
}

// 非 INVALID_API_KEY 的失败完全不碰这三列——归因只对「key 无效」这一类错误有意义。
func TestOpsErrorLoggerMiddlewareLeavesAttributionEmptyForOtherFailures(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	repo := &deletedKeyAuditOpsRepo{result: &service.DeletedKeyAuditResult{UserID: 9, KeyName: "unrelated"}}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"code": "INTERNAL_ERROR", "message": "boom"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-valid-key-0123456789")
	router.ServeHTTP(httptest.NewRecorder(), req)

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	entry := (<-opsErrorLogQueue).entry

	require.Empty(t, entry.AttemptedKeyPrefix)
	require.Nil(t, entry.DeletedKeyOwnerUserID)
	require.Empty(t, repo.lookedUp)
}
