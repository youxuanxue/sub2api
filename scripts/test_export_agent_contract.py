import tempfile
import unittest
from pathlib import Path

from scripts import export_agent_contract


class RetiredRouteContractTest(unittest.TestCase):
    def test_route_inventory_resolves_nested_groups_and_helper_parameters(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            routes_dir = root / "backend/internal/server/routes"
            routes_dir.mkdir(parents=True)
            (root / "backend/internal/server/router.go").write_text(
                '''package server

func registerRoutes(r *gin.Engine) {
    v1 := r.Group("/api/v1")
    routes.RegisterCustomRoutes(v1)
}
''',
                encoding="utf-8",
            )
            (routes_dir / "user.go").write_text(
                '''package routes

func RegisterCustomRoutes(v1 *gin.RouterGroup) {
    authenticated := v1.Group("")
    user := authenticated.Group("/user")
    registerTKUserRoutes(authenticated, user)
}

func RegisterUnusedRoutes(v1 *gin.RouterGroup) {
    v1.GET("/must-not-appear", handler)
}

func registerTKUserRoutes(authenticated, user *gin.RouterGroup) {
    user.POST("/onboarding-tour-completed", handler)
    authenticated.GET("/me/api-keys/:id/capabilities", handler)
}
''',
                encoding="utf-8",
            )

            routes = export_agent_contract.collect_http_routes(routes_dir)

            self.assertEqual(
                [(route.method, route.path, route.source) for route in routes],
                [
                    ("GET", "/api/v1/me/api-keys/:id/capabilities", "backend/internal/server/routes/user.go"),
                    ("POST", "/api/v1/user/onboarding-tour-completed", "backend/internal/server/routes/user.go"),
                ],
            )

    def test_generated_http_contract_contains_capability_route(self) -> None:
        routes = export_agent_contract.collect_http_routes(export_agent_contract.ROUTES_DIR)
        contract = export_agent_contract.render_http_contract(routes)

        self.assertIn(
            "- `GET /api/v1/me/api-keys/:id/capabilities` "
            "from `backend/internal/server/routes/user_tk_routes.go`",
            contract,
        )

    def test_all_registered_cli_entrypoints_load(self) -> None:
        for rel_path, factory_name in export_agent_contract.CLI_ENTRYPOINTS:
            with self.subTest(rel_path=rel_path):
                parser = export_agent_contract._load_argparse_parser(
                    rel_path, factory_name
                )
                self.assertIsNotNone(parser)

    def test_prune_retired_route_removes_only_tombstoned_bullet(self) -> None:
        doc = """\
- `GET /payment/channels` from `stale/generated/source.go`
- `GET /payment/checkout-info` from `backend/internal/server/routes/payment.go`
"""

        self.assertEqual(
            export_agent_contract.prune_retired_route_bullets(doc),
            "- `GET /payment/checkout-info` from `backend/internal/server/routes/payment.go`\n",
        )

    def test_source_registration_of_retired_route_is_rejected(self) -> None:
        route = {
            "method": "GET",
            "path": "/payment/channels",
            "source": "routes/payment.go",
            "source_literal": "/channels",
            "replacement": "/payment/checkout-info",
        }
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / route["source"]
            source.parent.mkdir(parents=True)
            source.write_text('authenticated.GET("/channels", handler)\n', encoding="utf-8")

            self.assertEqual(
                export_agent_contract.find_retired_source_registrations(root, (route,)),
                [route],
            )

    def test_retired_qa_traj_export_routes_point_at_bundle_replacements(self) -> None:
        replacements = {
            route["path"]: route["replacement"]
            for route in export_agent_contract.RETIRED_HTTP_ROUTES
            if route["path"].startswith("/api/v1/users/me/qa/")
        }
        self.assertEqual(
            replacements["/api/v1/users/me/qa/export"],
            "/api/v1/users/me/qa/bundles",
        )
        self.assertEqual(
            replacements["/api/v1/users/me/qa/traj/export"],
            "/api/v1/users/me/qa/bundles",
        )
        self.assertEqual(
            replacements["/api/v1/users/me/qa/traj/export/jobs"],
            "POST /api/v1/users/me/qa/bundles",
        )
        self.assertEqual(
            replacements["/api/v1/users/me/qa/traj/export/jobs/:job_id"],
            "/api/v1/users/me/qa/bundles/:job_id",
        )
        self.assertEqual(
            replacements["/api/v1/users/me/qa/traj/exports/*key"],
            "/api/v1/users/me/qa/bundle-exports/:job_id",
        )

    def test_resurrected_qa_traj_export_route_is_rejected(self) -> None:
        route = {
            "method": "POST",
            "path": "/api/v1/users/me/qa/traj/export",
            "source": "backend/internal/server/routes/user_tk_routes.go",
            "source_literal": "/users/me/qa/traj/export",
            "replacement": "/api/v1/users/me/qa/bundles",
        }
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / route["source"]
            source.parent.mkdir(parents=True)
            source.write_text('dualAuth.POST("/users/me/qa/traj/export", h.QA.ExportSelfTrajectory)\n', encoding="utf-8")

            self.assertEqual(
                export_agent_contract.find_retired_source_registrations(root, (route,)),
                [route],
            )


if __name__ == "__main__":
    unittest.main()
