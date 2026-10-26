// Command server is the kandev-fleet plugin backend.
package main

import "github.com/kandev/kandev/pkg/pluginsdk"

func main() {
	p := &fleetPlugin{}
	p.config = p.hostConfig
	pluginsdk.Serve(p)
}
