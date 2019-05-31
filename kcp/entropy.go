package kcp

import (
	"crypto/md5"
	"crypto/rand"
	"io"
)

// Entropy defines a entropy source
type Entropy interface {
	Init()
	Fill(nonce []byte)
}

// nonceMD5 nonce generator for packet header
type nonceMD5 struct {
	seed [md5.Size]byte
}

func (n *nonceMD5) Init() { /*nothing required*/ }

func (n *nonceMD5) Fill(nonce []byte) {
	if n.seed[0] == 0 { // entropy update
		io.ReadFull(rand.Reader, n.seed[:])
	}
	n.seed = md5.Sum(n.seed[:])
	copy(nonce, n.seed[:])
}
