package cli

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"rdcli/internal/authapi"
	"rdcli/internal/config"
)

func newAuthLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "login", Short: "log in to a self-hosted RustDesk API with an account",
		Args: exactArgs(0, "usage: rdcli auth login --api-server <url> --username <name> [--password-stdin]"),
		RunE: runAuthLogin,
	}
	cmd.Flags().String("api-server", "", "API URL (env: RDC_API_SERVER)")
	cmd.Flags().StringP("username", "u", "", "account username (env: RDC_API_USERNAME)")
	cmd.Flags().StringP("password", "p", "", "account password for login (env: RDC_API_PASSWORD)")
	cmd.Flags().Bool("password-stdin", false, "read the account password from stdin")
	cmd.Flags().Bool("allow-http", false, "allow password login to a trusted non-loopback HTTP API")
	return cmd
}

func loginValue(cmd *cobra.Command, flag, env string, defaults ...string) string {
	if value, _ := cmd.Flags().GetString(flag); value != "" {
		return value
	}
	if value := os.Getenv(env); value != "" {
		return value
	}
	for _, value := range defaults {
		if value != "" {
			return value
		}
	}
	return ""
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server := loginValue(cmd, "api-server", "RDC_API_SERVER", cfg.LoginServer, cfg.APIServer)
	username := strings.TrimSpace(loginValue(cmd, "username", "RDC_API_USERNAME", cfg.LoginUsername, cfg.APIUsername))
	if server == "" || username == "" {
		return usageErrorf("api login requires --api-server and --username (or RDC_API_SERVER/RDC_API_USERNAME or saved login settings)")
	}
	allowHTTP, _ := cmd.Flags().GetBool("allow-http")
	endpoint, err := authapi.Endpoint(server, allowHTTP)
	if err != nil {
		return usageErrorf("%s", err)
	}
	stdinPassword, _ := cmd.Flags().GetBool("password-stdin")
	if stdinPassword && cmd.Flags().Changed("password") {
		return usageErrorf("use either --password or --password-stdin")
	}
	password := loginValue(cmd, "password", "RDC_API_PASSWORD")
	if stdinPassword {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 65537))
		if err != nil {
			return fmt.Errorf("read account password from stdin: %w", err)
		}
		if len(data) > 65536 {
			return usageErrorf("account password input is too large")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if password == "" {
		return usageErrorf("provide an account password with --password-stdin, --password or RDC_API_PASSWORD")
	}
	uuid := cfg.LoginUUID
	if uuid == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return fmt.Errorf("generate login device UUID: %w", err)
		}
		raw[6] = (raw[6] & 0x0f) | 0x40
		raw[8] = (raw[8] & 0x3f) | 0x80
		uuid = fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	}
	token, err := authapi.Login(cmd.Context(), authapi.LoginOptions{
		Server: endpoint, Username: username, Password: password, UUID: uuid, AllowHTTP: allowHTTP, Timeout: getTimeoutFlag(cmd),
	})
	if err != nil {
		return err
	}
	cfg.SetAccessToken(token)
	cfg.LoginServer = endpoint
	cfg.LoginUsername = username
	cfg.LoginUUID = uuid
	if err := cfg.Save(); err != nil {
		return err
	}
	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Version   int    `json:"version"`
			LoggedIn  bool   `json:"logged_in"`
			Username  string `json:"username"`
			APIServer string `json:"api_server"`
			Config    string `json:"config"`
		}{1, true, username, endpoint, config.Path()})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "logged in as %s; token saved to %s\n", username, config.Path())
	return nil
}
