//go:build windows

package sqlserver

import (
	// shared memory and named pipes reach a local instance whose TCP/IP is
	// switched off (the usual SQL Server Express setup)
	_ "github.com/microsoft/go-mssqldb/namedpipe"
	_ "github.com/microsoft/go-mssqldb/sharedmemory"

	"golang.org/x/sys/windows/registry"
)

// localInstances lists the instances installed on this computer.
func localInstances() []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Microsoft SQL Server\Instance Names\SQL`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, _ := k.ReadValueNames(0)
	return names
}

// WindowsSignIn says whether a Windows sign-in can be tried.
const WindowsSignIn = true
