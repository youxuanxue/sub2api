.PHONY: build build-backend build-frontend build-datamanagementd test test-backend test-frontend test-frontend-lint test-frontend-typecheck test-frontend-critical test-datamanagementd

FRONTEND_CRITICAL_VITEST := \
	src/views/auth/__tests__/EmailVerifyView.spec.ts \
	src/views/__tests__/HomeView.compact.spec.ts \
	src/components/auth/__tests__/RegistrationActionTk.spec.ts \
	src/utils/__tests__/quickstartJourney.tk.spec.ts \
	src/components/keys/__tests__/UseKeyModal.spec.ts \
	src/views/user/__tests__/QuickstartView.spec.ts \
	src/views/user/__tests__/QuickstartView.pageChrome.spec.ts \
	src/views/auth/__tests__/LoginView.spec.ts \
	src/views/auth/__tests__/RegisterView.spec.ts \
	src/i18n/__tests__/localeKeyCompleteness.spec.ts \
	src/api/__tests__/client.spec.ts \
	src/api/__tests__/tokenRefresh.spec.ts \
	src/api/__tests__/channelMonitorV2.spec.ts \
	src/stores/__tests__/app.spec.ts \
	src/views/auth/__tests__/LinuxDoCallbackView.spec.ts \
	src/views/auth/__tests__/WechatCallbackView.spec.ts \
	src/views/user/__tests__/PaymentView.spec.ts \
	src/views/user/__tests__/PaymentResultView.spec.ts \
	src/views/user/__tests__/ChannelStatusView.mode.spec.ts \
	src/components/user/profile/__tests__/ProfileInfoCard.spec.ts \
	src/views/admin/__tests__/SettingsView.spec.ts \
	src/views/admin/__tests__/DashboardView.spec.ts \
	src/views/admin/__tests__/UsageView.spec.ts \
	src/views/user/__tests__/UsageView.spec.ts \
	src/api/__tests__/qaBundle.spec.ts \
	src/composables/__tests__/useTkQABundle.spec.ts \
	src/components/user/__tests__/UserDashboardStats.spec.ts

# 一键编译前后端
build: build-frontend build-backend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend: test-frontend-lint test-frontend-typecheck test-frontend-critical

test-frontend-lint:
	@pnpm --dir frontend run lint:check

test-frontend-typecheck:
	@pnpm --dir frontend run typecheck

test-frontend-critical:
	@pnpm --dir frontend exec vitest run $(FRONTEND_CRITICAL_VITEST)
	@node --test scripts/ci/test_frontend_eslint_ignore.mjs

test-datamanagementd:
	@cd datamanagement && go test ./...
