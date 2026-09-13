//go:build edge_handoff_e2e

// Local browser fixture: real handlers, signatures, Redis and AuthService;
// fixed user/inventory stores replace the production database and fleet discovery.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const prod = "http://127.0.0.1:4321"
const edge = "http://127.0.0.1:4322"

type users struct {
	service.UserRepository
	user *service.User
}

func (u *users) GetByID(context.Context, int64) (*service.User, error)          { return u.user, nil }
func (u *users) UpdateUserLastActiveAt(context.Context, int64, time.Time) error { return nil }

type fleet struct{ handoff *service.EdgeAdminHandoff }

func (f *fleet) Aggregate(context.Context, string) (*service.EdgeAccountsAggregate, error) {
	return inventory(), nil
}
func (f *fleet) AggregateFresh(context.Context, string) (*service.EdgeAccountsAggregate, error) {
	return inventory(), nil
}
func (f *fleet) AggregateByStub(context.Context) (*service.EdgeAccountsAggregate, error) {
	return inventory(), nil
}
func (f *fleet) AggregateByStubFresh(context.Context) (*service.EdgeAccountsAggregate, error) {
	return inventory(), nil
}
func (f *fleet) HandoffTarget(context.Context, string) (*service.EdgeHandoffTarget, error) {
	return &service.EdgeHandoffTarget{EdgeID: "local", URL: edge + "/admin/edge-handoff", Enabled: f.handoff.CanSign("local", edge)}, nil
}
func (f *fleet) MintAdminSession(ctx context.Context, id string, subject int64, input service.EdgeHandoffRequest) (*service.EdgeAdminSession, error) {
	envelope, err := f.handoff.Sign(id, edge, subject, input)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", edge+"/api/v1/edge/admin-handoff/mint", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("edge unavailable")
	}
	var env struct {
		Data service.EdgeHandoffCode `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return &service.EdgeAdminSession{EdgeID: id, Code: env.Data.Code, Attempt: env.Data.Attempt}, nil
}
func inventory() *service.EdgeAccountsAggregate {
	return &service.EdgeAccountsAggregate{Platform: "all", TS: 1, Edges: []service.EdgeAccountsResult{{EdgeID: "local", BaseURL: edge, OK: true, StubSchedulable: true, StubAccountID: 1, StubPlatform: "anthropic", Accounts: []json.RawMessage{}}}}
}
func main() {
	gin.SetMode(gin.ReleaseMode)
	root := os.Getenv("EDGE_HANDOFF_E2E_DIR")
	if root == "" {
		panic("EDGE_HANDOFF_E2E_DIR required")
	}
	dir := filepath.Join(root, "trust")
	must(os.Mkdir(dir, 0700))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	trust := config.EdgeHandoffConfig{Version: 1, Issuer: prod, Signers: map[string]config.EdgeHandoffSigner{"local": {Origin: edge, KeyID: "local", Seed: base64.RawURLEncoding.EncodeToString(key.Seed())}}, Receiver: &config.EdgeHandoffReceiver{Origin: edge, Issuer: prod, AdminUserID: 9, PublicKeys: map[string]string{"local": base64.RawURLEncoding.EncodeToString(pub)}}}
	raw, err := json.Marshal(trust)
	must(err)
	file := filepath.Join(dir, "trust.json")
	if os.Getenv("EDGE_HANDOFF_E2E_INVALID") == "1" {
		raw = []byte("invalid-trust-fixture")
	}
	must(os.WriteFile(file, raw, 0600))
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:4323"})
	defer rdb.Close()
	must(rdb.Ping(context.Background()).Err())
	handoff := service.NewEdgeAdminHandoff(&config.Config{EdgeHandoffFile: file}, repository.NewEdgeAdminHandoffCache(rdb))
	for _, isProd := range []bool{true, false} {
		id := int64(9)
		port := "127.0.0.1:4322"
		name := "Edge Administrator"
		if isProd {
			id = 7
			port = "127.0.0.1:4321"
			name = "Prod Administrator"
		}
		user := &service.User{ID: id, Email: "admin@example.test", Username: name, Role: service.RoleAdmin, Status: service.StatusActive, Concurrency: 10, AllowedGroups: []int64{}}
		userDTO := gin.H{"id": id, "email": user.Email, "username": name, "role": user.Role, "status": user.Status, "balance": 0, "concurrency": 10, "run_mode": "standard", "onboarding_tour_seen_at": "2026-09-12T00:00:00Z"}
		userRepo := &users{user: user}
		seed := make([]byte, 32)
		_, err := rand.Read(seed)
		must(err)
		cfg := &config.Config{JWT: config.JWTConfig{Secret: base64.RawURLEncoding.EncodeToString(seed), AccessTokenExpireMinutes: 30, RefreshTokenExpireDays: 7}}
		auth := service.NewAuthService(nil, userRepo, nil, repository.NewRefreshTokenCache(rdb), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
		userService := service.NewUserService(userRepo, nil, nil, nil)
		r := gin.New()
		r.Use(gin.Recovery(), middleware.SessionBindingContext(cfg))
		r.GET("/health", func(c *gin.Context) { c.Status(200) })
		r.GET("/setup/status", func(c *gin.Context) { c.JSON(200, gin.H{"needs_setup": false}) })
		r.GET("/api/v1/settings/public", func(c *gin.Context) {
			response.Success(c, gin.H{"site_name": "TokenKey", "backend_mode_enabled": true, "registration_enabled": false, "email_login_enabled": true, "password_login_enabled": true})
		})
		r.POST("/api/v1/auth/login", func(c *gin.Context) {
			var input struct{ Email, Password string }
			if c.ShouldBindJSON(&input) != nil || input.Email != "admin@example.test" || input.Password != "LocalUITest123!" {
				c.Status(401)
				return
			}
			pair, err := auth.GenerateTokenPair(c.Request.Context(), user, "")
			if err != nil {
				c.Status(503)
				return
			}
			response.Success(c, gin.H{"access_token": pair.AccessToken, "refresh_token": pair.RefreshToken, "expires_in": pair.ExpiresIn, "user": userDTO})
		})
		r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
			var input struct {
				RefreshToken string `json:"refresh_token"`
			}
			if c.ShouldBindJSON(&input) != nil || input.RefreshToken == "" {
				response.Error(c, 401, "no session")
				return
			}
			pair, err := auth.RefreshTokenPair(c.Request.Context(), input.RefreshToken)
			if err != nil {
				response.Error(c, 401, "invalid refresh")
				return
			}
			response.Success(c, pair.TokenPair)
		})
		r.GET("/api/v1/auth/me", gin.HandlerFunc(middleware.NewJWTAuthMiddleware(auth, userService, nil, nil)), func(c *gin.Context) { response.Success(c, userDTO) })
		h := handler.NewEdgeAdminSessionHandler(handoff, userRepo, auth)
		routes.RegisterTKEdgeRoutes(r.Group("/api/v1"), &handler.Handlers{EdgeAdminSession: h}, nil, nil, rdb)
		admin := r.Group("/api/v1/admin", gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, userService, nil, nil)))
		a := adminhandler.NewEdgeAccountsHandler(&fleet{handoff: handoff})
		admin.GET("/edge-accounts", a.List)
		admin.GET("/edge-accounts/:edge/handoff", a.HandoffTarget)
		admin.POST("/edge-accounts/:edge/admin-session", a.MintAdminSession)
		admin.GET("/accounts", func(c *gin.Context) {
			items := []any{}
			if isProd {
				items = append(items, gin.H{"id": 1, "name": "Local Edge", "edge_id": "local", "platform": "anthropic", "type": "apikey", "status": "active", "schedulable": true, "is_schedulable": true, "concurrency": 10, "priority": 1, "rate_multiplier": 1, "groups": []any{}, "credentials": gin.H{"base_url": edge}, "created_at": "2026-09-12T00:00:00Z"})
			}
			response.Success(c, gin.H{"items": items, "total": len(items), "page": 1, "page_size": 20, "pages": 1})
		})
		r.NoRoute(func(c *gin.Context) {
			if len(c.Request.URL.Path) >= 8 && c.Request.URL.Path[:8] == "/api/v1/" {
				response.Success(c, []any{})
				return
			}
			path := filepath.Join("internal/web/dist", filepath.Clean("/"+c.Request.URL.Path))
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				c.File(path)
				return
			}
			c.File("internal/web/dist/index.html")
		})
		go func() { must(http.ListenAndServe(port, r)) }()
	}
	select {}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
