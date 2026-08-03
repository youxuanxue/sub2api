package service

// Kiro streaming uses the shared pre-content keepalive helpers in
// gateway_service_tk_header_wait_keepalive.go (bindPreContentStreamKeepalive /
// stopPreContentStreamKeepalive). This file remains as a package-level pointer
// so sentinel inventories that historically tracked a Kiro-specific keepalive
// seam still have a stable TK companion path next to kiro_gateway_service.go.
