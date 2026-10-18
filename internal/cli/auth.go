package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"rdcli/internal/config"

	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "manage login token",
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "show login status",
		RunE:  runAuthStatus,
	}
	importCmd := &cobra.Command{
		Use:   "import-gui",
		Short: "import login token from RustDesk desktop app",
		RunE:  runAuthImport,
	}
	logoutCmd := &cobra.Command{
		Use:   "logout",
		Short: "clear stored login token",
		RunE:  runAuthLogout,
	}
	setCmd := &cobra.Command{
		Use:   "set <token>",
		Short: "set login token explicitly",
		Args:  exactArgs(1, "usage: rdcli auth set <token>"),
		RunE:  runAuthSet,
	}
	cmd.AddCommand(newAuthLoginCmd(), statusCmd, importCmd, logoutCmd, setCmd)
	return cmd
}

type authStatusJSON struct {
	Version     int    `json:"version"`
	LoggedIn    bool   `json:"logged_in"`
	Token       string `json:"token,omitempty"`
	Source      string `json:"source,omitempty"`
	GUIConfig   string `json:"gui_config,omitempty"`
	RDCConfig   string `json:"rdc_config,omitempty"`
	Server      string `json:"server,omitempty"`
	UserInfo    string `json:"user_info,omitempty"`
	PasswordSet bool   `json:"password_set"`
}

func runAuthStatus(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth status")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	guiTok := config.GUIAccessToken()
	guiPath := config.GUIAccessTokenPath()

	source := ""
	tok, _ := cmd.Flags().GetString("token")
	switch {
	case tok != "":
		source = "flag"
	case os.Getenv("RDC_TOKEN") != "":
		tok = os.Getenv("RDC_TOKEN")
		source = "env"
	case cfg.AccessToken != "":
		tok = cfg.AccessToken
		source = "config"
	case guiTok != "":
		tok = guiTok
		source = "gui"
	default:
		source = ""
	}

	st := authStatusJSON{
		Version:     1,
		LoggedIn:    tok != "",
		Token:       maskToken(tok),
		Source:      source,
		GUIConfig:   guiPath,
		RDCConfig:   config.Path(),
		Server:      resolveServer(cmd, cfg),
		PasswordSet: len(cfg.Peers) > 0,
	}
	if tok == "" {
		st.Token = ""
	}
	if source == "gui" {
		st.UserInfo = guiUserInfo(guiPath)
	}

	if isJSONFlag(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
	}

	if st.LoggedIn {
		fmt.Fprintf(cmd.OutOrStdout(), "logged in (%s)\n", source)
		fmt.Fprintf(cmd.OutOrStdout(), "token:    %s\n", maskToken(tok))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "not logged in")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "source:   %s\n", orDash(source))
	fmt.Fprintf(cmd.OutOrStdout(), "gui cfg:  %s\n", orDash(guiPath))
	fmt.Fprintf(cmd.OutOrStdout(), "rdc cfg:  %s\n", config.Path())
	if st.UserInfo != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "user:     %s\n", st.UserInfo)
	}
	if st.PasswordSet {
		fmt.Fprintf(cmd.OutOrStdout(), "saved passwords: %d peer(s)\n", len(cfg.Peers))
	}
	return nil
}

func runAuthImport(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth import-gui")
	}
	tok := config.GUIAccessToken()
	if tok == "" {
		return fmt.Errorf("auth: no token found in RustDesk desktop app config")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetAccessToken(tok)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "imported token from %s\n", config.GUIAccessTokenPath())
	return nil
}

func runAuthLogout(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("usage: rdcli auth logout")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AccessToken == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "no token stored")
		return nil
	}
	cfg.SetAccessToken("")
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "logged out")
	return nil
}

func runAuthSet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetAccessToken(args[0])
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "token saved")
	return nil
}

func maskToken(tok string) string {
	if len(tok) <= 8 {
		return "****"
	}
	return tok[:4] + "..." + tok[len(tok)-4:]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func guiUserInfo(guiPath string) string {
	if guiPath == "" {
		return ""
	}
	data, err := os.ReadFile(guiPath)
	if err != nil {
		return ""
	}
	inOptions := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			inOptions = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")) == "options"
			continue
		}
		if !inOptions {
			continue
		}
		if strings.HasPrefix(line, "user_info") {
			eq := strings.Index(line, "=")
			if eq >= 0 {
				v := strings.TrimSpace(line[eq+1:])
				return strings.Trim(v, "'\"")
			}
		}
	}
	return ""
}
