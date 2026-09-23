package service

import (
	"github.com/newo-ether/conch/encryptedhttp"
	"net/http"
)

type PublicServer struct {
	*Server
	channel http.Handler
}

func (s *PublicServer) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.channel.ServeHTTP(w, r) }

// NewPublicServer is the only supported public network entry point. Private
// loopback executor links retain their separate authentication and lifecycle.
func NewPublicServer(options ServerOptions) (*PublicServer, error) {
	application, err := NewServer(options)
	if err != nil {
		return nil, err
	}
	channel, err := encryptedhttp.New([]byte(options.Token), application)
	if err != nil {
		return nil, err
	}
	return &PublicServer{Server: application, channel: channel}, nil
}
