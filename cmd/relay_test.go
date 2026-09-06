package cmd

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	"github.com/spf13/viper"
)

func bindRelayTestConfig(t *testing.T) {
	t.Helper()
	viper.Reset()
	if err := viper.BindPFlags(rootCmd.Flags()); err != nil {
		t.Fatal(err)
	}
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	t.Cleanup(func() {
		viper.Reset()
		viper.BindPFlags(rootCmd.Flags())
		viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
		viper.AutomaticEnv()
	})
}

func TestRelayConfigDefaultsAndEnvironment(t *testing.T) {
	bindRelayTestConfig(t)
	for _, name := range []string{"RELAY_ENABLED", "RELAY_IDLE_TIMEOUT", "RELAY_RECONNECT_INITIAL", "RELAY_RECONNECT_MAX", "RELAY_READ_TIMEOUT"} {
		t.Setenv(name, "")
	}
	got, err := resolveRelayConfig()
	if err != nil || !reflect.DeepEqual(got, config.DefaultRelayConfig()) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	t.Setenv("RELAY_ENABLED", "false")
	t.Setenv("RELAY_IDLE_TIMEOUT", "0s")
	t.Setenv("RELAY_RECONNECT_INITIAL", "250ms")
	t.Setenv("RELAY_RECONNECT_MAX", "2s")
	t.Setenv("RELAY_READ_TIMEOUT", "3s")
	got, err = resolveRelayConfig()
	if err != nil || got.Enabled || got.IdleTimeout != 0 || got.ReconnectInitial != 250*time.Millisecond ||
		got.ReconnectMax != 2*time.Second || got.ReadTimeout != 3*time.Second {
		t.Fatalf("environment = %+v, %v", got, err)
	}
}

func TestRelayConfigFlagsOverrideEnvironment(t *testing.T) {
	bindRelayTestConfig(t)
	t.Setenv("RELAY_IDLE_TIMEOUT", "2s")
	t.Setenv("RELAY_ENABLED", "true")
	for name, value := range map[string]string{"relay-idle-timeout": "0s", "relay-enabled": "false"} {
		flag := rootCmd.Flags().Lookup(name)
		oldValue, oldChanged := flag.Value.String(), flag.Changed
		t.Cleanup(func() {
			flag.Value.Set(oldValue)
			flag.Changed = oldChanged
		})
		if err := rootCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveRelayConfig()
	if err != nil || got.Enabled || got.IdleTimeout != 0 {
		t.Fatalf("flags = %+v, %v", got, err)
	}
}

func TestRelayConfigRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"relay-enabled", "perhaps"},
		{"relay-idle-timeout", "tomorrow"},
		{"relay-idle-timeout", "-1s"},
		{"relay-reconnect-initial", "0s"},
		{"relay-reconnect-initial", "-1s"},
		{"relay-reconnect-max", "500ms"},
		{"relay-reconnect-max", "-1s"},
		{"relay-read-timeout", "0s"},
		{"relay-read-timeout", "-1s"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			bindRelayTestConfig(t)
			viper.Set(tc.key, tc.value)
			if _, err := resolveRelayConfig(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
