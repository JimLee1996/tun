package main

import (
	"github.com/JimLee1996/tun/kcp"
	"github.com/pkg/errors"
	"github.com/xtaci/tcpraw"
)

func listen(config *Config) (*kcp.Listener, error) {
	conn, err := tcpraw.Listen("tcp", config.Listen)
	if err != nil {
		return nil, errors.Wrap(err, "tcpraw.Listen()")
	}
	return kcp.ServeConn(conn)
}
