// Command probe is a hostile stand-in candidate used to prove the sandbox. Each
// argument names one action; the probe attempts it and prints one JSON object
// per line reporting whether the operating system allowed it.
//
// Actions: read:<path>, write:<path>, env:<name>, connect:<host:port>,
// listen:<host:port>, spawn.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// result reports one attempted action.
type result struct {
	// Action is the argument as given.
	Action string `json:"action"`
	// Allowed reports whether the action succeeded.
	Allowed bool `json:"allowed"`
	// Detail is the value read or the error returned.
	Detail string `json:"detail"`
}

func main() {
	enc := json.NewEncoder(os.Stdout)
	for _, arg := range os.Args[1:] {
		kind, target, _ := strings.Cut(arg, ":")
		r := result{Action: arg}
		err := attempt(kind, target, &r)
		r.Allowed = err == nil
		if err != nil {
			r.Detail = err.Error()
		}
		if encErr := enc.Encode(r); encErr != nil {
			fmt.Fprintln(os.Stderr, encErr)
			os.Exit(1)
		}
	}
}

// attempt performs one action and records what it observed.
func attempt(kind, target string, r *result) error {
	switch kind {
	case "read":
		b, err := os.ReadFile(target)
		r.Detail = string(b)
		return err
	case "write":
		return os.WriteFile(target, []byte("probe"), 0o600)
	case "env":
		v, ok := os.LookupEnv(target)
		if !ok {
			return fmt.Errorf("%s is not set", target)
		}
		r.Detail = v
		return nil
	case "connect":
		c, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			return err
		}
		return c.Close()
	case "listen":
		ln, err := net.Listen("tcp", target)
		if err != nil {
			return err
		}
		return ln.Close()
	case "spawn":
		return exec.Command("/bin/echo", "spawned").Run()
	default:
		return fmt.Errorf("unknown action %q", kind)
	}
}
