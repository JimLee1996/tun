package main

import (
	"github.com/JimLee1996/tun/kcp"
	"github.com/JimLee1996/tun/tcpraw"
	"github.com/pkg/errors"
)

func listen(config *Config) (*kcp.Listener, error) {
	conn, err := tcpraw.Listen("tcp", config.Listen)
	if err != nil {
		return nil, errors.Wrap(err, "tcpraw.Listen()")
	}
	return kcp.ServeConn(conn)
}
