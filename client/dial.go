package main

import (
	"github.com/JimLee1996/tun/kcp"
	"github.com/JimLee1996/tun/tcpraw"
	"github.com/pkg/errors"
)

func dial(config *Config) (*kcp.UDPSession, error) {
	conn, err := tcpraw.Dial("tcp", config.RemoteAddr)
	if err != nil {
		return nil, errors.Wrap(err, "tcpraw.Dial()")
	}
	return kcp.NewConn(config.RemoteAddr, conn)
}
