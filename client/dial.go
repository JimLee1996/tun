package main

import (
	"github.com/JimLee1996/tun/kcp"
	"github.com/pkg/errors"
	"github.com/xtaci/tcpraw"
)

func dial(config *Config) (*kcp.UDPSession, error) {
	conn, err := tcpraw.Dial("tcp", config.RemoteAddr)
	if err != nil {
		return nil, errors.Wrap(err, "tcpraw.Dial()")
	}
	return kcp.NewConn(config.RemoteAddr, conn)
}
