package main

import (
	"flag"
	"fmt"

	"sharetoken/internal/client"
)

// apikey shows or changes the OpenAI endpoint's API key (internal/client/apikey.go).
// It works on the file directly: a running client reads it again by itself.
//
//	apikey [-q]        the endpoint, whether the key is required, and the key (-q: the key only, for scripts)
//	apikey on | off    require the key, or let any request in
//	apikey rotate      a new key; the old one stops working
func (c *cmdCtx) apikey(args []string) error {
	sub := ""
	if len(args) > 0 && (args[0] == "on" || args[0] == "off" || args[0] == "rotate" || args[0] == "show") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("apikey", flag.ExitOnError)
	quiet := fs.Bool("q", false, "print the key only")
	_ = fs.Parse(args)

	var s client.APIKeySettings
	var err error
	switch sub {
	case "", "show":
		s, err = client.LoadAPIKey(c.home)
	case "on", "off":
		s, err = client.SetAPIKeyRequired(c.home, sub == "on")
	case "rotate":
		s, err = client.RotateAPIKey(c.home)
	}
	if err != nil {
		return err
	}
	if *quiet {
		fmt.Println(s.Key)
		return nil
	}
	addr, _ := client.LocalAPI(c.home, c.cfg)
	fmt.Printf("endpoint  http://%s/v1\n", addr)
	if s.Required {
		fmt.Println("api key   required (Authorization: Bearer <key>; `sharetoken apikey off` lets any request in)")
	} else {
		fmt.Println("api key   not required: any program or user of this machine can spend your credits here (`sharetoken apikey on`)")
	}
	fmt.Printf("key       %s\n", s.Key)
	if sub == "rotate" {
		fmt.Println("the old key no longer works: put the new one in the tools that use this endpoint")
	}
	return nil
}
