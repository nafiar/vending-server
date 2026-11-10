package http

import "net/http"

// Every route the server answers, in one place. The path is the contract the
// admin web and the Kiosk Agent are written against, and the wrapper each one
// is given (public, admin, machineOnly, operatorOnly) is who is allowed to call
// it: reading down this file is how you see the whole API and its access rules
// at once.

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)

	// Admin web (UC-06 to UC-12).
	m.Handle("POST /api/admin/login", s.public(s.adminLogin))
	m.Handle("POST /api/admin/logout", s.public(s.adminLogout))
	m.Handle("GET /api/admin/me", s.admin(s.me))
	m.Handle("GET /api/admin/recap/summary", s.admin(s.summary))
	m.Handle("GET /api/admin/recap", s.admin(s.recap))
	m.Handle("GET /api/admin/transactions", s.admin(s.transactions))
	m.Handle("GET /api/admin/restock-sessions", s.admin(s.restockSessions))
	m.Handle("GET /api/admin/products", s.admin(s.products))
	m.Handle("POST /api/admin/products", s.admin(s.createProduct))
	m.Handle("PUT /api/admin/products/{id}", s.admin(s.updateProduct))
	m.Handle("POST /api/admin/products/{id}/archive", s.admin(s.archiveProduct))
	m.Handle("POST /api/admin/products/{id}/restore", s.admin(s.restoreProduct))
	m.Handle("GET /api/admin/machines", s.admin(s.machines))
	m.Handle("POST /api/admin/machines", s.admin(s.createMachine))
	m.Handle("GET /api/admin/machines/{id}", s.admin(s.machine))
	m.Handle("POST /api/admin/machines/{id}/activate", s.admin(s.setMachineActive(true)))
	m.Handle("POST /api/admin/machines/{id}/deactivate", s.admin(s.setMachineActive(false)))
	m.Handle("POST /api/admin/machines/{id}/rotate-key", s.admin(s.rotateMachineKey))
	m.Handle("PUT /api/admin/machines/{id}/slots/{n}", s.admin(s.updateSlot))
	m.Handle("GET /api/admin/operators", s.admin(s.operators))
	m.Handle("POST /api/admin/operators", s.admin(s.createOperator))
	m.Handle("POST /api/admin/operators/{id}/activate", s.admin(s.setOperatorActive(true)))
	m.Handle("POST /api/admin/operators/{id}/deactivate", s.admin(s.setOperatorActive(false)))
	m.Handle("POST /api/admin/operators/{id}/reset-password", s.admin(s.resetOperatorPassword))
	m.Handle("GET /api/admin/audit-logs", s.admin(s.auditLogs))
	m.Handle("GET /api/admin/reconcile", s.admin(s.reconcile))
	m.Handle("POST /api/admin/demo/reset", s.admin(s.demoReset))

	// Kiosk Agent (UC-01 to UC-05). The agent forwards the kiosk's calls here, adding the machine key.
	m.Handle("GET /api/machine/catalog", s.machineOnly(s.catalog))
	m.Handle("POST /api/machine/heartbeat", s.machineOnly(s.heartbeat))
	m.Handle("POST /api/machine/transactions", s.machineOnly(s.createTransaction))
	m.Handle("GET /api/machine/transactions/{id}", s.machineOnly(s.transactionStatus))
	m.Handle("POST /api/machine/transactions/{id}/pay", s.machineOnly(s.pay))
	m.Handle("POST /api/machine/transactions/{id}/result", s.machineOnly(s.result))
	m.Handle("POST /api/machine/transactions/{id}/cancel", s.machineOnly(s.cancel))
	m.Handle("POST /api/machine/transactions/{id}/report", s.machineOnly(s.report))
	m.Handle("POST /api/machine/operator/login", s.machineOnly(s.operatorLogin))
	m.Handle("POST /api/machine/operator/logout", s.machineOnly(s.operatorLogout))
	m.Handle("GET /api/machine/operator/slots", s.operatorOnly(s.operatorSlots))
	m.Handle("POST /api/machine/restock-sessions", s.operatorOnly(s.saveRestock))

	// Files.
	m.Handle("GET /uploads/", http.StripPrefix("/uploads/", uploadsHandler(s.opts.UploadDir)))
	m.Handle("GET /", s.static())
}
