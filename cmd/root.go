/*
 * Iptv-Proxy is a project to proxyfie an m3u file and to proxyfie an Xtream iptv service (client API).
 * Copyright (C) 2020  Pierre-Emmanuel Jacquier
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package cmd

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/server"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "iptv-proxy",
	Short: "Reverse proxy on iptv m3u file and xtream codes server api",
	Run: func(cmd *cobra.Command, args []string) {

		log.Printf("[iptv-proxy] Server is starting...")

		m3uSources := resolveM3USources()
		remoteHostURL := &url.URL{}
		if len(m3uSources) == 1 {
			var err error
			remoteHostURL, err = url.Parse(m3uSources[0])
			if err != nil {
				log.Fatal(err)
			}
		}

		xtreamUser := viper.GetString("xtream-user")
		xtreamPassword := viper.GetString("xtream-password")
		xtreamBaseURL := viper.GetString("xtream-base-url")

		var username, password string
		if len(m3uSources) == 1 && strings.Contains(m3uSources[0], "/get.php") {
			username = remoteHostURL.Query().Get("username")
			password = remoteHostURL.Query().Get("password")
		}

		if err := validateXtreamSourceConfig(m3uSources, xtreamUser, xtreamPassword, xtreamBaseURL); err != nil {
			log.Fatal(err)
		}

		if len(m3uSources) == 1 && xtreamBaseURL == "" && xtreamPassword == "" && xtreamUser == "" {
			if username != "" && password != "" {
				log.Printf("[iptv-proxy] INFO: It's seams you are using an Xtream provider!")

				xtreamUser = username
				xtreamPassword = password
				xtreamBaseURL = fmt.Sprintf("%s://%s", remoteHostURL.Scheme, remoteHostURL.Host)
				log.Printf("[iptv-proxy] INFO: xtream service enable with xtream base url: %q xtream username: %q xtream password: %q", xtreamBaseURL, xtreamUser, xtreamPassword)
			}
		}

		config.DebugLoggingEnabled = viper.GetBool("debug-logging")
		config.CacheFolder = viper.GetString("cache-folder")
		if config.CacheFolder != "" {
			// Ensure CacheFolder ends with a '/'
			if config.CacheFolder != "" && !strings.HasSuffix(config.CacheFolder, "/") {
				config.CacheFolder += "/"
			}
		}

		includeGroups := getStringSliceSetting("include-group")

		conf := &config.ProxyConfig{
			HostConfig: &config.HostConfiguration{
				Hostname: viper.GetString("hostname"),
				Port:     viper.GetInt("port"),
			},
			RemoteURL:            remoteHostURL,
			XtreamUser:           config.CredentialString(xtreamUser),
			XtreamPassword:       config.CredentialString(xtreamPassword),
			XtreamBaseURL:        xtreamBaseURL,
			M3UCacheExpiration:   viper.GetInt("m3u-cache-expiration"),
			User:                 config.CredentialString(viper.GetString("user")),
			Password:             config.CredentialString(viper.GetString("password")),
			AdvertisedPort:       viper.GetInt("advertised-port"),
			HTTPS:                viper.GetBool("https"),
			M3UFileName:          viper.GetString("m3u-file-name"),
			M3USources:           m3uSources,
			IncludeGroups:        includeGroups,
			ListGroups:           viper.GetBool("list-groups"),
			CustomEndpoint:       viper.GetString("custom-endpoint"),
			CustomId:             viper.GetString("custom-id"),
			XtreamGenerateApiGet: viper.GetBool("xtream-api-get"),
			RelayEnabled:         viper.GetBool("relay-enabled"),
			RelayBufferDuration:  viper.GetDuration("relay-buffer-duration"),
			RelayTargetDelay:     viper.GetDuration("relay-target-delay"),
			RelayIdleTimeout:     viper.GetDuration("relay-idle-timeout"),
			RelayReconnectDelay:  viper.GetDuration("relay-reconnect-delay"),
			RelayReconnectMax:    viper.GetDuration("relay-reconnect-max"),
			RelayMaxBufferBytes:  viper.GetInt("relay-max-buffer-bytes"),
			RelayLogSummaryEvery: viper.GetDuration("relay-log-summary-interval"),
			RelayLogVerbose:      viper.GetBool("relay-log-verbose"),
		}

		if conf.AdvertisedPort == 0 {
			conf.AdvertisedPort = conf.HostConfig.Port
		}

		server, err := server.NewServer(conf)
		if err != nil {
			log.Fatal(err)
		}

		if conf.ListGroups {
			for _, group := range server.Groups() {
				fmt.Println(group)
			}
			return
		}

		if e := server.Serve(); e != nil {
			log.Fatal(e)
		}
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "iptv-proxy-config", "C", "", "Config file (default is $HOME/.iptv-proxy.yaml)")
	rootCmd.Flags().StringP("m3u-url", "u", "", `Legacy single-source M3U file or url e.g: "http://example.com/iptv.m3u"`)
	rootCmd.Flags().StringSlice("m3u-source", nil, `Repeatable M3U source file or url`)
	rootCmd.Flags().StringP("m3u-file-name", "", "iptv.m3u", `Name of the new proxified m3u file e.g "http://poxy.com/iptv.m3u"`)
	rootCmd.Flags().StringSlice("include-group", nil, `Repeatable M3U group-title/category to keep in the merged output`)
	rootCmd.Flags().Bool("list-groups", false, "List discovered M3U groups/categories and exit")
	rootCmd.Flags().StringP("custom-endpoint", "", "", `Custom endpoint "http://poxy.com/<custom-endpoint>/iptv.m3u"`)
	rootCmd.Flags().StringP("custom-id", "", "", `Custom anti-collison ID for each track "http://proxy.com/<custom-id>/..."`)
	rootCmd.Flags().Int("port", 8080, "Iptv-proxy listening port")
	rootCmd.Flags().Int("advertised-port", 0, "Port to expose the IPTV file and xtream (by default, it's taking value from port) useful to put behind a reverse proxy")
	rootCmd.Flags().String("hostname", "", "Hostname or IP to expose the IPTVs endpoints")
	rootCmd.Flags().BoolP("https", "", false, "Activate https for urls proxy")
	rootCmd.Flags().String("user", "usertest", "User auth to access proxy (m3u/xtream)")
	rootCmd.Flags().String("password", "passwordtest", "Password auth to access proxy (m3u/xtream)")
	rootCmd.Flags().String("xtream-user", "", "Xtream-code user login")
	rootCmd.Flags().String("xtream-password", "", "Xtream-code password login")
	rootCmd.Flags().String("xtream-base-url", "", "Xtream-code base url e.g(http://expample.tv:8080)")
	rootCmd.Flags().Int("m3u-cache-expiration", 1, "M3U cache expiration in hour")
	rootCmd.Flags().BoolP("xtream-api-get", "", false, "Generate get.php from xtream API instead of get.php original endpoint")
	rootCmd.Flags().Bool("relay-enabled", true, "Enable shared buffered relay for eligible non-HLS live TS streams")
	rootCmd.Flags().Duration("relay-buffer-duration", 10*time.Second, "Buffered relay retention window for eligible live streams")
	rootCmd.Flags().Duration("relay-target-delay", 4*time.Second, "Target playback delay behind the live edge for buffered relay")
	rootCmd.Flags().Duration("relay-idle-timeout", 30*time.Second, "How long an idle relay session stays alive after the last viewer disconnects")
	rootCmd.Flags().Duration("relay-reconnect-delay", 250*time.Millisecond, "Initial reconnect backoff for buffered relay sessions")
	rootCmd.Flags().Duration("relay-reconnect-max", 5*time.Second, "Maximum reconnect backoff for buffered relay sessions")
	rootCmd.Flags().Int("relay-max-buffer-bytes", 32*1024*1024, "Maximum in-memory relay buffer size per active channel")
	rootCmd.Flags().Duration("relay-log-summary-interval", time.Minute, "Interval for buffered relay summary logs (0 disables periodic summary)")
	rootCmd.Flags().Bool("relay-log-verbose", false, "Enable verbose per-request buffered relay logs")

	if e := viper.BindPFlags(rootCmd.Flags()); e != nil {
		log.Fatal("error binding PFlags to viper")
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Search config in home directory with name ".iptv-proxy" (without extension).
		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigName(".iptv-proxy")
	}

	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}

func resolveM3USources() []string {
	sources := getStringSliceSetting("m3u-source")
	if len(sources) > 0 {
		return sources
	}

	legacySource := strings.TrimSpace(viper.GetString("m3u-url"))
	if legacySource == "" {
		return nil
	}

	return []string{legacySource}
}

func getStringSliceSetting(key string) []string {
	values := normalizeStringSlice(viper.GetStringSlice(key))
	if len(values) > 0 {
		return values
	}

	rawValue := strings.TrimSpace(viper.GetString(key))
	if rawValue == "" {
		return nil
	}

	return normalizeStringSlice([]string{rawValue})
}

func normalizeStringSlice(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		for _, splitValue := range splitPipeSeparatedValue(value) {
			trimmedValue := strings.TrimSpace(splitValue)
			if trimmedValue == "" {
				continue
			}

			normalized = append(normalized, trimmedValue)
		}
	}

	return normalized
}

func splitPipeSeparatedValue(value string) []string {
	splitValues := make([]string, 0, 1)
	var current strings.Builder
	escaped := false

	for _, r := range value {
		if escaped {
			if r != '|' && r != '\\' {
				current.WriteRune('\\')
			}
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch r {
		case '\\':
			escaped = true
		case '|':
			splitValues = append(splitValues, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}

	if escaped {
		current.WriteRune('\\')
	}

	splitValues = append(splitValues, current.String())

	return splitValues
}

func validateXtreamSourceConfig(m3uSources []string, xtreamUser, xtreamPassword, xtreamBaseURL string) error {
	if len(m3uSources) <= 1 {
		return nil
	}

	if xtreamUser == "" && xtreamPassword == "" && xtreamBaseURL == "" {
		return nil
	}

	return errors.New("xtream proxy mode supports a single M3U source only")
}
