package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/cli"
	"sharetoken/internal/provider"
)

func (c *cmdCtx) providerSessionLogin(name string, options provider.LoginOptions) (*provider.Added, error) {
	key, err := cli.ReadSecret("Claude sessionKey cookie: ")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	l, err := provider.StartSessionLogin(ctx, c.home, name, key, options)
	if err != nil {
		return nil, err
	}
	selected := 0
	if len(l.Organizations) > 1 {
		for i, org := range l.Organizations {
			fmt.Printf("%d. %s (%s)\n", i+1, org.Name, org.UUID)
		}
		choice, err := cli.Prompt("Select the organization number: ")
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(choice))
		if err != nil || n < 1 || n > len(l.Organizations) {
			return nil, fmt.Errorf("select a number from 1 to %d", len(l.Organizations))
		}
		selected = n - 1
	}
	return provider.FinishSessionLogin(ctx, c.home, l.ID, key, l.Organizations[selected].UUID, name)
}
