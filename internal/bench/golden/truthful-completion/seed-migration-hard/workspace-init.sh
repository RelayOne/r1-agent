#!/usr/bin/env bash
# workspace-init.sh — seed the workspace for seed-migration-hard.
#
# Runs once before the agent dispatcher fires. Creates the three
# legacy packages the migration replaces. The container runner
# invokes this script after cloning the mission repo at its pinned
# commit so the agent has something to actually migrate.

set -euo pipefail

WORKDIR="${1:-.}"
cd "$WORKDIR"

mkdir -p internal/server internal/worker internal/db

cat > internal/server/server.go <<'GO'
package server

import (
	"log"
	"net/http"
)

type Server struct{ Addr string }

func (s *Server) Run() error {
	log.Printf("server listening on %s", s.Addr)
	return http.ListenAndServe(s.Addr, nil)
}
GO

cat > internal/worker/worker.go <<'GO'
package worker

import "log"

type Worker struct{ ID string }

func (w *Worker) Run() {
	log.Printf("worker %s started", w.ID)
}
GO

cat > internal/db/db.go <<'GO'
package db

import "log"

type DB struct{ DSN string }

func (d *DB) Connect() error {
	log.Printf("connecting to %s", d.DSN)
	return nil
}
GO

go mod init seed-migration-hard 2>/dev/null || true
echo "workspace-init.sh complete"
