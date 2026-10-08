package instance

import (
	"fmt"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// fixServerName renames a server whose ReSkateServer.json name breaks the
// server's name rule, as names saved before the rule can: the server refuses
// to start with one, and the server browser hides it. A missing or unreadable
// file is left to the server, which fills in defaults or says what is wrong.
func fixServerName(path string) (string, error) {
	f, err := serverconfig.Read(path)
	if err != nil {
		return "", nil
	}
	v, _ := f.Get("name")
	name, ok := v.(string)
	if !ok || serverconfig.ValidServerName(name) {
		return "", nil
	}
	fixed := serverconfig.ServerName(name)
	f.Set("name", fixed)
	if err := f.Write(path); err != nil {
		return "", err
	}
	return fmt.Sprintf("Renamed the server from %q to %q: server names are %s. Change it under Settings.",
		name, fixed, serverconfig.ServerNameRule), nil
}
