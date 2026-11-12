//go:build !windows

package sqlserver

func localInstances() []string { return nil }

// WindowsSignIn says whether a Windows sign-in can be tried.
const WindowsSignIn = false
