package proxy

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flare-foundation/tee-proxy/internal/metrics"
	"github.com/flare-foundation/tee-proxy/pkg/config"
)

// TestDirectConfigCopiesAllFields ensures every /direct configuration field, in particular
// the body-size limit, reaches the server's DirectConfig.
func TestDirectConfigCopiesAllFields(t *testing.T) {
	c := config.Direct{
		Enable:         true,
		APIKey:         "secret",
		APIKeyOptional: true,
		MaxBodySize:    1234,
	}

	got := directConfig(c)

	require.Equal(t, c.Enable, got.Enable)
	require.Equal(t, c.APIKey, got.APIKey)
	require.Equal(t, c.APIKeyOptional, got.APIKeyOptional)
	require.Equal(t, c.MaxBodySize, got.MaxBodySize)
}

func ptr(b bool) *bool { return &b }

// TestMetricsConfig pins the opt-in/inherit semantics documented on config.Metrics:
// a disabled master switch forces every group off; when enabled an unset group
// inherits enable and an explicit value wins.
func TestMetricsConfig(t *testing.T) {
	t.Run("disabled forces every group off", func(t *testing.T) {
		got := metricsConfig(config.Metrics{Enable: false, HTTP: ptr(true), Runtime: ptr(true)})
		require.Equal(t, metrics.Config{}, got, "no group may be on when the master switch is off")
	})

	t.Run("enabled with unset groups turns them all on", func(t *testing.T) {
		got := metricsConfig(config.Metrics{Enable: true})
		require.Equal(t, metrics.Config{
			Enable: true, HTTP: true, Storage: true, Queue: true, Voting: true,
			ActiveVoters: true, Result: true, Info: true, Attestation: true,
			Policy: true, Liveness: true, Node: true, Runtime: true,
		}, got)
	})

	t.Run("explicit false omits just that group", func(t *testing.T) {
		got := metricsConfig(config.Metrics{Enable: true, Storage: ptr(false), Policy: ptr(false)})
		require.True(t, got.Enable)
		require.False(t, got.Storage, "explicit false must omit the group")
		require.False(t, got.Policy, "explicit false must omit the group")
		require.True(t, got.HTTP, "an unset group still inherits enable")
		require.True(t, got.Runtime, "an unset group still inherits enable")
	})

	t.Run("explicit true is on", func(t *testing.T) {
		got := metricsConfig(config.Metrics{Enable: true, HTTP: ptr(true)})
		require.True(t, got.HTTP)
	})
}

// TestMetricsConfigGroupParity guards against group drift: every group must be declared in
// both config.Metrics (*bool) and metrics.Config (bool) and be wired through metricsConfig.
// Adding a group to one place but not the others fails here instead of silently shipping a
// group that can't be toggled or never collects.
func TestMetricsConfigGroupParity(t *testing.T) {
	boolPtr := reflect.TypeFor[*bool]()

	cfgGroups := map[string]struct{}{}
	ct := reflect.TypeFor[config.Metrics]()
	for i := 0; i < ct.NumField(); i++ {
		if ct.Field(i).Type == boolPtr {
			cfgGroups[ct.Field(i).Name] = struct{}{}
		}
	}

	mcGroups := map[string]struct{}{}
	mt := reflect.TypeFor[metrics.Config]()
	for i := 0; i < mt.NumField(); i++ {
		f := mt.Field(i)
		if f.Type.Kind() == reflect.Bool && f.Name != "Enable" {
			mcGroups[f.Name] = struct{}{}
		}
	}

	require.Equal(t, cfgGroups, mcGroups,
		"config.Metrics (*bool fields) and metrics.Config (bool fields) must declare the same group set")

	// Master on, all groups unset: the resolver must turn every group on. A group missing
	// from metricsConfig stays false and is caught here.
	resolved := reflect.ValueOf(metricsConfig(config.Metrics{Enable: true}))
	for name := range mcGroups {
		require.True(t, resolved.FieldByName(name).Bool(),
			"metricsConfig left group %s off with everything enabled — is it wired in the resolver?", name)
	}
}
