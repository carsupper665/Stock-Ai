//go:build wintest

package gateway

// winTest is on only in binaries built with -tags wintest, the Windows test package: no login,
// and the middleware serves the frontend itself. Release binaries cannot turn it on.
const winTest = true
