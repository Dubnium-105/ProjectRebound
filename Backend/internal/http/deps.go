package http

import (
	"database/sql"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/store"
)

type Deps struct {
	DB        *sql.DB
	Config    *config.Config
	NatStore  *store.NatTraversalStore
	RelayStore *store.RelayStore
	ProbeSender ProbeSender
}

// ProbeSender sends UDP probe packets for host reachability verification.
type ProbeSender interface {
	Send(host string, port int, nonce string) error
}
