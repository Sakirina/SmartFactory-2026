// Package observability exposes the same telemetry contract as DataTransfer.
package observability

import shared "competition2026/product/datatransfer/observability"

type Config = shared.Config
type Runtime = shared.Runtime
type Identity = shared.Identity
type Option = shared.Option

var New = shared.New
var ConfigFromEnv = shared.ConfigFromEnv
var StartOperation = shared.StartOperation
var WithIdentity = shared.WithIdentity
var LogAttrs = shared.LogAttrs
var Extract = shared.Extract
var Inject = shared.Inject
var ExtractHTTP = shared.ExtractHTTP
var InjectHTTP = shared.InjectHTTP
var HTTPContext = shared.HTTPContext
var UnaryServerInterceptor = shared.UnaryServerInterceptor
var UnaryClientInterceptor = shared.UnaryClientInterceptor
var WithSpanExporter = shared.WithSpanExporter
var WithMetricReader = shared.WithMetricReader
