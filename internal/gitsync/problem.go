// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitsync

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Problems a repository can have, as Check.Problem names them; the UI says each in words.
const (
	ProblemAddress     = "address"     // not an address of a repository
	ProblemNoHost      = "no_host"     // the host name does not resolve
	ProblemUnreachable = "unreachable" // the host does not answer
	ProblemTimeout     = "timeout"
	ProblemTLS         = "tls"       // the certificate is not trusted
	ProblemAuth        = "auth"      // the token is missing, wrong or without access
	ProblemNotFound    = "not_found" // no such repository, or private without a token
	ProblemNoBranch    = "no_branch"
)

// Problem names what went wrong with a repository, or "" when it is none of the known things.
// Transports do not always keep the error chain, so the message is looked at too.
func Problem(err error) string {
	if err == nil {
		return ""
	}
	var (
		dns  *net.DNSError
		op   *net.OpError
		unk  x509.UnknownAuthorityError
		inv  x509.CertificateInvalidError
		host x509.HostnameError
	)
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return ProblemAuth
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return ProblemNotFound
	case errors.Is(err, errNoBranch):
		return ProblemNoBranch
	case errors.As(err, &dns), strings.Contains(msg, "no such host"):
		return ProblemNoHost
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return ProblemTimeout
	case errors.As(err, &unk), errors.As(err, &inv), errors.As(err, &host), strings.Contains(msg, "x509:"):
		return ProblemTLS
	case errors.As(err, &op), strings.Contains(msg, "connection refused"), strings.Contains(msg, "network is unreachable"):
		return ProblemUnreachable
	}
	return ""
}
