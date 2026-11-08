package config

import (
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration is a time.Duration written as "30s", "10m", "720h" (or 0).
type Duration time.Duration

// D parses a duration literal; it panics on a bad one (for defaults).
func D(s string) Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return Duration(d)
}

// T is the value as a time.Duration.
func (d Duration) T() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == "0" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return &yaml.TypeError{Errors: []string{"line " + strconv.Itoa(n.Line) + ": bad duration " + n.Value + " (want e.g. 30s, 10m, 720h)"}}
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
func (d Duration) String() string            { return time.Duration(d).String() }
func (d *Duration) Set(s string) error {
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}
