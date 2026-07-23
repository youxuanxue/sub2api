package main

//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
)

//go:embed VERSION
var embeddedVersion string

// Build-time variables (can be set by ldflags)
var (
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// initLogger configures the default slog handler based on gin.Mode().
// In non-release mode, Debug level logs are enabled.
func main() {
	logger.InitBootstrap()
	defer logger.Sync()

	// Parse command line flags
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	// Some imported dependencies (e.g. new-api/common) also register "-version"
	// during init(). Reuse existing registration to avoid duplicate-flag panic.
	var showVersionFlag *bool
	if flag.Lookup("version") == nil {
		showVersionFlag = flag.Bool("version", false, "Show version information")
	}
	flag.Parse()

	showVersion := false
	if showVersionFlag != nil {
		showVersion = *showVersionFlag
	} else if existing := flag.Lookup("version"); existing != nil {
		parsed, err := strconv.ParseBool(existing.Value.String())
		if err == nil {
			showVersion = parsed
		}
	}

	if showVersion {
		log.Printf("TokenKey %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	// CLI setup mode
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// Check if setup is needed
	if setup.NeedsSetup() {
		// Check if auto-setup is enabled (for Docker deployment)
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// Continue to main server after auto-setup
		} else {
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// Normal server mode
	runMainServer()
}

func runSetupServer() {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	// Register setup routes
	setup.RegisterRoutes(r)

	// Serve embedded frontend if available
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Get server address from config.yaml or environment variables (SERVER_HOST, SERVER_PORT)
	// This allows users to run setup on a different address if needed
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure TokenKey")

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

func runMainServer() {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Cleanup()
	if app.PromptAudit != nil {
		if err := app.PromptAudit.Start(context.Background()); err != nil {
			// Startup continues so unrelated APIs stay up. Fail-closed (unavailable)
			// applies only when a persisted blocking policy was observed; without
			// blocking intent, Prompt Audit stays ModeOff so the gateway remains
			// usable and administrators can still disable the feature (#4560).
			log.Printf("Prompt Audit started in degraded state: %v", err)
		}
	}

	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()
	if !service.IsEdgeFrontendURL(cfg.Server.FrontendURL) {
		service.StartClaudeStatusPoller(pollCtx)
	}

	// 启动服务器
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	// SIGUSR1：进入 drain 模式，但不退出。
	// 发版流程用 `docker kill -s USR1 tokenkey` 触发，让 /health 立刻翻 503，
	// Caddy 的 passive health 据此摘除 upstream；进程继续把已经在跑的请求处理完。
	// 这样在真正 stop 之前给了 SSE 等长流自然结束的窗口。
	drainCh := make(chan os.Signal, 1)
	signal.Notify(drainCh, syscall.SIGUSR1)
	go func() {
		for range drainCh {
			if middleware.IsDraining() {
				log.Println("SIGUSR1 received; already draining")
				continue
			}
			middleware.SetDrain(true)
			log.Println("SIGUSR1 received; drain mode activated (/health -> 503)")
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// SIGTERM 兜底：即便 deploy 没发 SIGUSR1，停机前也把 drain 拉起，让外层
	// 反代有机会从最新的 /health 看到 503 后立刻摘除 upstream（仅几百毫秒级窗口，
	// 不影响排空，但避免「stop 瞬间客户端拿到 502」的尖刺）。
	middleware.SetDrain(true)
	log.Println("Shutting down server...")

	shutdownTimeout := resolveShutdownTimeout()
	log.Printf("Graceful shutdown timeout=%s", shutdownTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := app.Server.Shutdown(ctx); err != nil {
		// 注意：这里改成非 Fatal，避免在已经接收到的请求还没全部退出时被 cancel
		// 误判为「致命」。docker stop 会有自己的 stop_grace_period 兜底。
		log.Printf("Server shutdown returned: %v (in_flight=%d)", err, middleware.InFlightCount())
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

// resolveShutdownTimeout 读取 TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS（默认 120s）。
// docker-compose 端配 stop_grace_period=180s，留 30s 余量给 docker 自己收尾。
func resolveShutdownTimeout() time.Duration {
	const defaultTimeout = 120 * time.Second
	raw := strings.TrimSpace(os.Getenv("TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultTimeout
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		log.Printf("Invalid TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS=%q, falling back to %s", raw, defaultTimeout)
		return defaultTimeout
	}
	return time.Duration(secs) * time.Second
}
