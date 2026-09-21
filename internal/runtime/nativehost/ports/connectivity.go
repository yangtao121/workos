package ports

import "time"

// Connectivity carries a short-lived transport capability, never shared keys.
type Connectivity struct {
	Mode      string
	Servers   []IceServer
	RelayOnly bool
	ExpiresAt time.Time
}

type IceServer struct {
	URLs       []string
	Username   string
	Credential string
}

type ConnectivityIssuer interface {
	Issue() (Connectivity, error)
}
