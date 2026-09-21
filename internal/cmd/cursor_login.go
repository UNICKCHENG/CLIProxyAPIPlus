package cmd

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoCursorLogin imports a Cursor API key and saves it as an auth file.
//
// The key comes from options.Metadata["api_key"] (the --cursor-api-key flag) or, in a TTY
// session, from an interactive prompt. The process environment is deliberately not consulted.
// There is no browser sign-in to drive: a Cursor credential is a key the operator creates in
// the dashboard, so the command's job is to accept that key, prove it works, and record which
// account it belongs to.
func DoCursorLogin(cfg *config.Config, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	promptFn := options.Prompt
	if promptFn == nil {
		promptFn = defaultProjectPrompt()
	}

	manager := newAuthManager()
	authOpts := &sdkAuth.LoginOptions{
		NoBrowser:    options.NoBrowser,
		CallbackPort: options.CallbackPort,
		Metadata: map[string]string{
			"api_key": options.APIKey,
		},
		Prompt: promptFn,
	}

	record, savedPath, err := manager.Login(context.Background(), "cursor", cfg, authOpts)
	if err != nil {
		log.Errorf("Cursor authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}
	if record != nil && record.Label != "" {
		fmt.Printf("Authenticated as %s\n", record.Label)
	}
	fmt.Println("Cursor authentication successful! Revoke or rotate the key in the Cursor dashboard, then run --cursor-login again.")
}
